// Package sudoplan takes over the managed user's sudo access so glocker's
// schedule is the only way to root, and gives it back exactly on uninstall.
//
// glocker never edits /etc/sudoers. It owns one drop-in, /etc/sudoers.d/glocker,
// holding either the allowed or the blocked line. Whatever the distro used to
// give the first user sudo — an admin group, a per-user drop-in — is taken away
// at install and recorded, so uninstall can put back what was there.
//
// Support is explicit: each distro family has a Plan below. Anything else gets
// no takeover, and so no sudo window (blocking and the rest still work).
package sudoplan

import (
	"bufio"
	"os"
	"strings"
)

// Plan is how one distro family grants its first user sudo.
type Plan struct {
	Name string
	// IDs are matched against os-release ID first, then ID_LIKE.
	IDs []string
	// AdminGroups are the groups this distro hands out for sudo. Membership is
	// only taken over when sudoers gives the group a blanket grant, so a group
	// that only carries scoped commands (Omarchy's wheel) is left alone.
	AdminGroups []string
}

// Every plan also moves aside per-user blanket drop-ins under sudoers.d — the
// file archinstall writes, and the NOPASSWD file cloud images add.
var Plans = []Plan{
	// Ubuntu (and derivatives with ID_LIKE=ubuntu): the installer adds the user
	// to `sudo` (`%sudo ALL=(ALL:ALL) ALL`); releases before 12.04 used `admin`.
	{Name: "ubuntu", IDs: []string{"ubuntu"}, AdminGroups: []string{"sudo", "admin"}},
	// Fedora: "Make this user administrator" adds the user to wheel
	// (`%wheel ALL=(ALL) ALL`).
	{Name: "fedora", IDs: []string{"fedora"}, AdminGroups: []string{"wheel"}},
	// Omarchy: a per-user sudoers.d/NN_<user> file grants `ALL=(ALL) ALL`; wheel
	// only carries scoped NOPASSWD commands, which stay.
	{Name: "omarchy", IDs: []string{"omarchy"}, AdminGroups: []string{"wheel"}},
	// Arch: archinstall writes sudoers.d/NN_<user> like Omarchy; a hand install
	// usually uncomments `%wheel ALL=(ALL:ALL) ALL` instead. Both are covered.
	// Not tested on a real Arch machine.
	{Name: "arch", IDs: []string{"arch"}, AdminGroups: []string{"wheel"}},
}

// Detect picks the plan for the system's os-release. ok is false for an
// unsupported distro; id is the os-release ID either way, for messages.
func Detect(s System) (p Plan, id string, ok bool) {
	vals := readOSRelease(s.OSRelease)
	id = vals["ID"]
	candidates := append([]string{id}, strings.Fields(vals["ID_LIKE"])...)
	for _, c := range candidates {
		for _, p := range Plans {
			for _, pid := range p.IDs {
				if c == pid {
					return p, id, true
				}
			}
		}
	}
	return Plan{}, id, false
}

func readOSRelease(path string) map[string]string {
	out := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, found := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if !found || strings.HasPrefix(k, "#") {
			continue
		}
		out[k] = strings.Trim(v, `"'`)
	}
	return out
}
