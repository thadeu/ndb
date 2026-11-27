package replication_test

import (
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The kill loop runs real daemons: an owner, a copy that pulls it through a
// TCP proxy, and a writer that keeps the acks. It kills the owner and the
// copy with SIGKILL and cuts the network, then checks that both end with
// the same data and that no acked write is lost.

func buildNDB(t *testing.T) string {
	t.Helper()

	bin := filepath.Join(t.TempDir(), "ndb")

	out, err := exec.Command("go", "build", "-o", bin, "../../cmd/ndb").CombinedOutput()
	if err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	return bin
}

func freePort(t *testing.T) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	defer ln.Close()

	return ln.Addr().String()
}

// daemon is one ndb serve process.
type daemon struct {
	t    *testing.T
	bin  string
	dir  string
	addr string
	env  []string
	cmd  *exec.Cmd
	log  *os.File
}

func newDaemon(t *testing.T, bin, addr string, env ...string) *daemon {
	t.Helper()

	dir := t.TempDir()

	for name, secret := range map[string]string{"app.token": appToken, "repl.token": replToken} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(secret), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	yml := "tokens:\n  - name: app\n    file: app.token\n    admin: [\"*\"]\n  - name: repl\n    file: repl.token\n    replicate: true\n"

	if err := os.WriteFile(filepath.Join(dir, "ndb.yml"), []byte(yml), 0o600); err != nil {
		t.Fatal(err)
	}

	d := &daemon{t: t, bin: bin, dir: dir, addr: addr, env: append(env, "NDB_ENCRYPTION_KEY="+testKey)}

	t.Cleanup(d.kill)

	return d
}

func (d *daemon) start() {
	d.t.Helper()

	log, err := os.OpenFile(filepath.Join(d.dir, "daemon.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		d.t.Fatal(err)
	}

	cmd := exec.Command(d.bin, "serve", "-config", filepath.Join(d.dir, "ndb.yml"), "-data-dir", filepath.Join(d.dir, "data"), "-unix", "", "-tcp", d.addr, "-log-level", "warn")
	cmd.Env = append(os.Environ(), d.env...)
	cmd.Stdout, cmd.Stderr = log, log

	if err := cmd.Start(); err != nil {
		d.t.Fatal(err)
	}

	d.cmd, d.log = cmd, log

	deadline := time.Now().Add(20 * time.Second)

	for time.Now().Before(deadline) {
		if status, _, err := call(d.addr, "GET", "/_health", ""); err == nil && status == http.StatusOK {
			return
		}

		time.Sleep(20 * time.Millisecond)
	}

	data, _ := os.ReadFile(filepath.Join(d.dir, "daemon.log"))
	d.t.Fatalf("the daemon did not start:\n%s", data)
}

func (d *daemon) kill() {
	if d.cmd == nil {
		return
	}

	_ = d.cmd.Process.Kill()
	_ = d.cmd.Wait()
	d.log.Close()

	d.cmd = nil
}

// call is one request with the app token.
func call(addr, method, path, body string) (int, []byte, error) {
	req, err := http.NewRequest(method, "http://"+addr+path, strings.NewReader(body))
	if err != nil {
		return 0, nil, err
	}

	req.Header.Set("Authorization", "Bearer "+appToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}

	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)

	return resp.StatusCode, data, err
}

// allDocs is every document of coll on addr, sorted, or nil on an error.
func allDocs(addr, coll string) []string {
	status, body, err := call(addr, "POST", "/"+coll+"/_search", `{"limit":100000}`)
	if err != nil || status != http.StatusOK {
		return nil
	}

	var rows []json.RawMessage

	if json.Unmarshal(body, &rows) != nil {
		return nil
	}

	out := make([]string, 0, len(rows))

	for _, d := range rows {
		out = append(out, string(d))
	}

	sort.Strings(out)

	return out
}

// proxy forwards TCP to target; cut closes every open connection.
type proxy struct {
	ln     net.Listener
	target string

	mu    sync.Mutex
	conns []net.Conn
}

func newProxy(t *testing.T, target string) *proxy {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	p := &proxy{ln: ln, target: target}

	go p.accept()

	t.Cleanup(func() { ln.Close(); p.cut() })

	return p
}

func (p *proxy) accept() {
	for {
		c, err := p.ln.Accept()
		if err != nil {
			return
		}

		go p.pipe(c)
	}
}

func (p *proxy) pipe(c net.Conn) {
	up, err := net.Dial("tcp", p.target)
	if err != nil {
		c.Close()

		return
	}

	p.mu.Lock()
	p.conns = append(p.conns, c, up)
	p.mu.Unlock()

	go func() { _, _ = io.Copy(up, c); up.Close() }()

	_, _ = io.Copy(c, up)
	c.Close()
}

func (p *proxy) cut() {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, c := range p.conns {
		c.Close()
	}

	p.conns = nil
}

func TestKillLoopLosesNoAckedWrite(t *testing.T) {
	if testing.Short() {
		t.Skip("kill loop: real daemons, kill -9 and network cuts")
	}

	bin := buildNDB(t)

	owner := newDaemon(t, bin, freePort(t))
	owner.start()

	px := newProxy(t, owner.addr)
	cp := newDaemon(t, bin, freePort(t), "NDB_REPLICA_OF=tcp://:"+replToken+"@"+px.ln.Addr().String())
	cp.start()

	var (
		acked   sync.Map
		stop    atomic.Bool
		wg      sync.WaitGroup
		written atomic.Int64
	)

	wg.Add(1)

	go func() {
		defer wg.Done()

		for i := 0; !stop.Load(); i++ {
			id := fmt.Sprintf("d%05d", i%400)
			body := fmt.Sprintf(`{"docs":[{"_id":%q,"n":%d}],"upsert":true}`, id, i)

			status, _, err := call(owner.addr, "POST", "/k/_bulk", body)

			if err == nil && status == http.StatusOK {
				acked.Store(id, i)
				written.Add(1)
			} else {
				time.Sleep(10 * time.Millisecond)
			}

			if i%97 == 0 {
				_, _, _ = call(owner.addr, "DELETE", "/k/_doc/"+fmt.Sprintf("d%05d", (i/97)%400), "")
				acked.Delete(fmt.Sprintf("d%05d", (i/97)%400))
			}
		}
	}()

	for round := range 30 {
		time.Sleep(time.Duration(100+rand.IntN(300)) * time.Millisecond)

		switch round % 3 {
		case 0:
			cp.kill()
			cp.start()
		case 1:
			px.cut()
		case 2:
			owner.kill()
			owner.start()
		}
	}

	stop.Store(true)
	wg.Wait()

	deadline := time.Now().Add(30 * time.Second)

	var want, got []string

	for time.Now().Before(deadline) {
		want, got = allDocs(owner.addr, "k"), allDocs(cp.addr, "k")

		if want != nil && equal(want, got) {
			break
		}

		time.Sleep(100 * time.Millisecond)
	}

	if !equal(want, got) {
		t.Fatalf("the copy has %d docs, the owner %d", len(got), len(want))
	}

	have := map[string]bool{}

	for _, d := range want {
		var doc struct {
			ID string `json:"_id"`
		}

		_ = json.Unmarshal([]byte(d), &doc)
		have[doc.ID] = true
	}

	lost := 0

	acked.Range(func(k, _ any) bool {
		if !have[k.(string)] {
			lost++
		}

		return true
	})

	if lost > 0 {
		t.Fatalf("%d acked documents are not on the owner", lost)
	}

	t.Logf("%d acked writes, %d docs on both", written.Load(), len(want))
}
