package config

// SetUserHome makes the defaults of a user install (home != "") or of a
// server install (home == ""), and gives back the old behavior.
func SetUserHome(home string) (restore func()) {
	old := userHomeFunc

	userHomeFunc = func() (string, bool) { return home, home != "" }

	return func() { userHomeFunc = old }
}
