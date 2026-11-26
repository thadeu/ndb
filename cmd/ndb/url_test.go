package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestURLCases runs spec/url/cases.json, the NDB_URL cases of every client.
// The CLI has no token option, so a case with one is skipped.
func TestURLCases(t *testing.T) {
	b, err := os.ReadFile("../../spec/url/cases.json")
	if err != nil {
		t.Fatal(err)
	}

	var file struct {
		Cases []struct {
			Name   string  `json:"name"`
			URL    string  `json:"url"`
			Env    *string `json:"env"`
			Option *string `json:"option"`
			Secret string  `json:"secret"`
			Want   struct {
				Token *string `json:"token"`
				Error bool    `json:"error"`
			} `json:"want"`
		} `json:"cases"`
	}

	if err := json.Unmarshal(b, &file); err != nil {
		t.Fatal(err)
	}

	for _, c := range file.Cases {
		t.Run(c.Name, func(t *testing.T) {
			if c.Option != nil {
				t.Skip("the CLI has no token option")
			}

			env := ""

			if c.Env != nil {
				env = *c.Env
			}

			t.Setenv("NDB_TOKEN", env)

			client, err := newAPIClient(c.URL)

			if c.Want.Error {
				if err == nil {
					t.Fatalf("newAPIClient(%q): no error", c.URL)
				}

				if c.Secret != "" && strings.Contains(err.Error(), c.Secret) {
					t.Fatalf("the error shows the token: %v", err)
				}

				return
			}

			if err != nil {
				t.Fatal(err)
			}

			want := ""

			if c.Want.Token != nil {
				want = *c.Want.Token
			}

			if client.token != want {
				t.Fatalf("token %q, want %q", client.token, want)
			}

			if c.Secret != "" && strings.Contains(redactURL(c.URL), c.Secret) {
				t.Fatalf("the source shows the token: %s", redactURL(c.URL))
			}

			if shown := fmt.Sprintf("%v %+v", client, client); c.Secret != "" && strings.Contains(shown, c.Secret) {
				t.Fatalf("the printed client shows the token: %s", shown)
			}
		})
	}
}
