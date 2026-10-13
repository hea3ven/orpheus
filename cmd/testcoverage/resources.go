package main

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
)

// resourceEnvironment fails closed on unobservable constraints. The CI runner
// must expose its complete cgroup v2 ancestry; v1 and hidden mount roots cannot
// provide the evidence this gate requires.
func resourceEnvironment(readFile func(string) ([]byte, error)) (map[string]string, error) {
	result := make(map[string]string)
	for _, path := range []string{"/proc/self/limits", "/proc/self/cgroup", "/proc/sys/kernel/random/boot_id"} {
		if err := recordProbe(result, readFile, path); err != nil {
			return result, err
		}
	}
	if result["/proc/self/limits"] == "" || result["/proc/sys/kernel/random/boot_id"] == "" {
		return result, errors.New("empty process limits or runner identity probe")
	}
	for path, fields := range map[string][]string{
		"/proc/self/status": {"Cpus_allowed_list:", "Mems_allowed_list:"},
		"/proc/meminfo":     {"MemTotal:"},
		"/proc/cpuinfo":     {"model name", "Hardware"},
	} {
		contents, err := readFile(path)
		if err != nil {
			return result, fmt.Errorf("resource probe %s: %w", path, err)
		}
		values := selectedResourceLines(string(contents), fields)
		if values == "" {
			return result, fmt.Errorf("resource probe %s has no required fields", path)
		}
		if path == "/proc/self/status" {
			for _, field := range fields {
				if !strings.Contains(values, field) {
					return result, fmt.Errorf("resource probe %s is missing %s", path, field)
				}
			}
		}
		result[path] = values
	}
	mounts, err := readFile("/proc/self/mountinfo")
	if err != nil {
		return result, fmt.Errorf("resource mounts: %w", err)
	}
	root, err := cgroupMount(string(mounts))
	if err != nil {
		return result, err
	}
	result["cgroup_mount"] = root
	membership := strings.TrimSpace(result["/proc/self/cgroup"])
	if !strings.HasPrefix(membership, "0::/") || strings.Contains(membership, "\n") {
		return result, errors.New("resource probe requires unified cgroup v2 membership")
	}
	group := strings.TrimPrefix(membership, "0::")
	if filepath.Clean(group) != group {
		return result, errors.New("invalid cgroup membership path")
	}
	if err := recordCgroupAncestry(result, readFile, root, group); err != nil {
		return result, err
	}
	return result, nil
}

func selectedResourceLines(contents string, fields []string) string {
	var selected []string
	for _, line := range strings.Split(contents, "\n") {
		for _, field := range fields {
			if strings.HasPrefix(line, field) {
				selected = append(selected, line)
				break
			}
		}
	}
	return strings.Join(selected, "\n")
}

func cgroupMount(contents string) (string, error) {
	for _, line := range strings.Split(contents, "\n") {
		before, after, ok := strings.Cut(line, " - ")
		if !ok || !strings.HasPrefix(after, "cgroup2 ") {
			continue
		}
		fields := strings.Fields(before)
		if len(fields) < 5 || fields[3] != "/" || strings.Contains(fields[4], "\\") {
			return "", errors.New("cgroup mount hides ancestors or has an unsupported path")
		}
		return fields[4], nil
	}
	return "", errors.New("no verified cgroup v2 mount; resource limits are unknown")
}

func recordCgroupAncestry(result map[string]string, readFile func(string) ([]byte, error), root, group string) error {
	if err := recordProbe(result, readFile, filepath.Join(root, "cgroup.controllers")); err != nil {
		return err
	}
	if err := verifyResourceRoot(result, readFile, root); err != nil {
		return err
	}
	path := root
	parentControllers := ""
	parts := strings.Split(strings.TrimPrefix(group, "/"), "/")
	for index := -1; index < len(parts); index++ {
		if index >= 0 {
			if parts[index] == "" {
				continue
			}
			path = filepath.Join(path, parts[index])
			if err := recordControllerLimits(result, readFile, path, parentControllers); err != nil {
				return err
			}
		}
		subtree := filepath.Join(path, "cgroup.subtree_control")
		if err := recordProbe(result, readFile, subtree); err != nil {
			return err
		}
		parentControllers = result[subtree]
	}
	return nil
}

// Only the kernel hierarchy root lacks cgroup.type and quota interfaces. A
// namespace-rooted non-root cgroup still exposes them: reject it rather than
// treating hidden ancestor limits as unlimited.
func verifyResourceRoot(result map[string]string, readFile func(string) ([]byte, error), root string) error {
	for _, file := range []string{"cgroup.type", "cpu.max", "memory.max", "pids.max"} {
		_, err := readFile(filepath.Join(root, file))
		if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("cannot verify kernel cgroup root: %s is present or inaccessible", file)
		}
	}
	result["cgroup_root_constraints"] = "verified kernel root exemption (root mount, no non-root interfaces)"
	controllers := " " + result[filepath.Join(root, "cgroup.controllers")] + " "
	if strings.Contains(controllers, " cpuset ") {
		for _, file := range []string{"cpuset.cpus.effective", "cpuset.mems.effective"} {
			if err := recordProbe(result, readFile, filepath.Join(root, file)); err != nil {
				return err
			}
		}
	}
	return nil
}

func recordControllerLimits(result map[string]string, readFile func(string) ([]byte, error), path, controllers string) error {
	active := " " + strings.Join(strings.Fields(controllers), " ") + " "
	for controller, files := range map[string][]string{
		"cpu":    {"cpu.max", "cpu.weight"},
		"cpuset": {"cpuset.cpus.effective", "cpuset.mems.effective"},
		"memory": {"memory.max", "memory.high", "memory.swap.max"},
		"pids":   {"pids.max"},
	} {
		if !strings.Contains(active, " "+controller+" ") {
			result[filepath.Join(path, controller+".absence")] = "verified: not enabled in parent subtree_control"
			continue
		}
		for _, file := range files {
			if err := recordProbe(result, readFile, filepath.Join(path, file)); err != nil {
				return err
			}
		}
	}
	return nil
}

func recordProbe(result map[string]string, readFile func(string) ([]byte, error), path string) error {
	contents, err := readFile(path)
	if err != nil {
		return fmt.Errorf("resource probe %s: %w", path, err)
	}
	result[path] = strings.TrimSpace(string(contents))
	return nil
}
