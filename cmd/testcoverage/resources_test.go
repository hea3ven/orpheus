package main

import (
	"io/fs"
	"strings"
	"testing"
)

func resourceFixture() map[string]string {
	return map[string]string{
		"/proc/sys/kernel/random/boot_id":                "fixture-runner-identity",
		"/proc/self/limits":                              "Max processes 4096 4096 processes",
		"/proc/self/cgroup":                              "0::/team/job\n",
		"/proc/self/status":                              "Name: collector\nCpus_allowed_list:\t0-3\nMems_allowed_list:\t0\n",
		"/proc/meminfo":                                  "MemTotal: 1234 kB\nMemFree: 12 kB\n",
		"/proc/cpuinfo":                                  "model name : fixture CPU\n",
		"/proc/self/mountinfo":                           "1 0 0:1 / /sys/fs/cgroup rw - cgroup2 cgroup2 rw\n",
		"/sys/fs/cgroup/cpuset.cpus.effective":           "0-3",
		"/sys/fs/cgroup/cpuset.mems.effective":           "0",
		"/sys/fs/cgroup/cgroup.controllers":              "cpu memory pids cpuset",
		"/sys/fs/cgroup/cgroup.subtree_control":          "cpu memory pids cpuset",
		"/sys/fs/cgroup/team/cpu.max":                    "200000 100000",
		"/sys/fs/cgroup/team/cpu.weight":                 "100",
		"/sys/fs/cgroup/team/cpuset.cpus.effective":      "0-3",
		"/sys/fs/cgroup/team/cpuset.mems.effective":      "0",
		"/sys/fs/cgroup/team/memory.max":                 "1073741824",
		"/sys/fs/cgroup/team/memory.high":                "536870912",
		"/sys/fs/cgroup/team/memory.swap.max":            "0",
		"/sys/fs/cgroup/team/pids.max":                   "1024",
		"/sys/fs/cgroup/team/cgroup.subtree_control":     "",
		"/sys/fs/cgroup/team/job/cgroup.subtree_control": "",
	}
}

func fixtureResourceReader(files map[string]string) func(string) ([]byte, error) {
	return func(path string) ([]byte, error) {
		value, found := files[path]
		if !found {
			return nil, fs.ErrNotExist
		}
		return []byte(value), nil
	}
}

func TestResourceProbeRetainsAncestorConstraintsAndVerifiedLeafAbsence(t *testing.T) {
	files := resourceFixture()

	got, err := resourceEnvironment(fixtureResourceReader(files))

	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/sys/fs/cgroup/team/cpu.max", "/sys/fs/cgroup/team/memory.max", "/sys/fs/cgroup/team/memory.high", "/sys/fs/cgroup/team/pids.max", "/sys/fs/cgroup/team/cpuset.cpus.effective"} {
		if got[path] != files[path] {
			t.Fatalf("ancestor probe %s = %q, want %q", path, got[path], files[path])
		}
	}
	if !strings.HasPrefix(got["/sys/fs/cgroup/team/job/cpu.absence"], "verified:") {
		t.Fatalf("leaf absence lacks evidence: %#v", got)
	}
	if strings.Contains(got["/proc/meminfo"], "MemFree") || strings.Contains(got["/proc/self/status"], "collector") {
		t.Fatal("probe included volatile resource usage")
	}
}

func TestResourceProbeMissingFilesNeverImplyUnlimitedResources(t *testing.T) {
	for _, path := range []string{"/proc/self/limits", "/proc/sys/kernel/random/boot_id", "/proc/self/status", "/proc/self/cgroup", "/proc/self/mountinfo", "/sys/fs/cgroup/cgroup.controllers", "/sys/fs/cgroup/cgroup.subtree_control", "/sys/fs/cgroup/team/cpu.max", "/sys/fs/cgroup/team/memory.max", "/sys/fs/cgroup/team/job/cgroup.subtree_control"} {
		t.Run(path, func(t *testing.T) {
			files := resourceFixture()
			delete(files, path)

			_, err := resourceEnvironment(fixtureResourceReader(files))

			if err == nil {
				t.Fatalf("missing probe %s was accepted as unlimited", path)
			}
		})
	}
}

func TestResourceProbeRejectsHiddenAncestryAndUnsupportedMembership(t *testing.T) {
	for _, test := range []struct{ name, path, value string }{
		{name: "missing affinity field", path: "/proc/self/status", value: "Cpus_allowed_list: 0-3"},
		{name: "empty host identity", path: "/proc/sys/kernel/random/boot_id", value: ""},
		{name: "hidden ancestors", path: "/proc/self/mountinfo", value: "1 0 0:1 /hidden /sys/fs/cgroup rw - cgroup2 cgroup2 rw"},
		{name: "missing controller mount", path: "/proc/self/mountinfo", value: ""},
		{name: "v1 membership", path: "/proc/self/cgroup", value: "3:cpu:/team/job"},
		{name: "hybrid membership", path: "/proc/self/cgroup", value: "0::/team/job\n3:cpu:/team/job"},
		{name: "invalid traversal", path: "/proc/self/cgroup", value: "0::/team/../job"},
	} {
		t.Run(test.name, func(t *testing.T) {
			files := resourceFixture()
			files[test.path] = test.value

			_, err := resourceEnvironment(fixtureResourceReader(files))

			if err == nil {
				t.Fatal("unobservable resource constraints were accepted")
			}
		})
	}
}

func TestResourceProbeAcceptsVerifiedControllerAbsence(t *testing.T) {
	files := resourceFixture()
	files["/sys/fs/cgroup/cgroup.controllers"] = ""
	files["/sys/fs/cgroup/cgroup.subtree_control"] = ""

	got, err := resourceEnvironment(fixtureResourceReader(files))

	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got["/sys/fs/cgroup/team/memory.absence"], "verified:") {
		t.Fatal("verified controller absence was not recorded")
	}
}

func TestResourceProbeRejectsNamespaceRootThatCanHideAncestorQuotas(t *testing.T) {
	files := resourceFixture()
	files["/sys/fs/cgroup/cgroup.type"] = "domain"
	files["/sys/fs/cgroup/cpu.max"] = "max 100000"

	_, err := resourceEnvironment(fixtureResourceReader(files))

	if err == nil {
		t.Fatal("namespace-rooted hierarchy accepted without ancestor evidence")
	}
}
