package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestInterleavedPackageEventsSumWorkWithoutDoubleCountingSubtests(t *testing.T) {
	input := strings.NewReader(strings.Join([]string{
		`{"Action":"run","Package":"example.test/a","Test":"TestA"}`,
		`{"Action":"run","Package":"example.test/b","Test":"TestB"}`,
		`{"Action":"run","Package":"example.test/a","Test":"TestA/child"}`,
		`{"Action":"pass","Package":"example.test/a","Test":"TestA/child","Elapsed":1}`,
		`{"Action":"pass","Package":"example.test/b","Test":"TestB","Elapsed":2}`,
		`{"Action":"pass","Package":"example.test/b","Elapsed":3}`,
		`{"Action":"pass","Package":"example.test/no-tests","Elapsed":10}`,
		`{"Action":"pass","Package":"example.test/a","Test":"TestA","Elapsed":2}`,
		`{"Action":"pass","Package":"example.test/a","Elapsed":4}`,
	}, "\n"))

	events := decodeTestEvents(input)
	timings := sortedTimings(events.packages)

	if events.decodeErr != nil {
		t.Fatal(events.decodeErr)
	}
	want := []packageTiming{{Name: "example.test/a", Seconds: 4}, {Name: "example.test/b", Seconds: 3}}
	if !reflect.DeepEqual(timings, want) || selectedTestSeconds(timings) != 7 || events.testCount != 3 {
		t.Fatalf("decoded work = %#v, %d tests; want %#v, 7 seconds, 3 tests", timings, events.testCount, want)
	}
}

func TestQualityPolicyIgnoresCommandWallTimeForDecisionsAndUpdates(t *testing.T) {
	policy := testLocalQualityPolicy()
	serial := testQualityReport()
	concurrent := cloneReport(t, serial)
	for _, name := range laneNames {
		lane := serial.Lanes[name]
		lane.WallSeconds = 1000
		serial.Lanes[name] = lane
		lane.WallSeconds = 0.01
		concurrent.Lanes[name] = lane
	}

	serialDecision := assessQualityPolicy(policy, serial)
	concurrentDecision := assessQualityPolicy(policy, concurrent)
	serialPolicy, serialChanges := updatedQualityPolicy(policy, serial)
	concurrentPolicy, concurrentChanges := updatedQualityPolicy(policy, concurrent)

	if serialDecision.Status != statusPass || !reflect.DeepEqual(serialDecision, concurrentDecision) {
		t.Fatalf("wall time changed decision: serial %#v, concurrent %#v", serialDecision, concurrentDecision)
	}
	if !reflect.DeepEqual(serialPolicy, concurrentPolicy) || !reflect.DeepEqual(serialChanges, concurrentChanges) {
		t.Fatal("wall time changed proposed policy bounds")
	}
}

func TestPolicySamplesRejectMixedSchedulingButAllowTemporaryProfilePaths(t *testing.T) {
	samples := []qualityReport{testQualityReport(), testQualityReport()}
	for index := range samples {
		for _, name := range laneNames {
			lane := samples[index].Lanes[name]
			lane.Command = coverageCommand(name, []string{"first.cover", "second.cover"}[index], "")
			samples[index].Lanes[name] = lane
		}
	}
	if _, err := aggregatePolicySamples(samples); err != nil {
		t.Fatalf("comparable commands with different temporary profiles: %v", err)
	}

	lane := samples[1].Lanes["unit"]
	lane.Command = append(lane.Command[:len(lane.Command)-1], "-p=1", "./...")
	samples[1].Lanes["unit"] = lane

	if _, err := aggregatePolicySamples(samples); err == nil || !strings.Contains(err.Error(), "test command changed") {
		t.Fatalf("mixed scheduling error = %v, want test command changed", err)
	}
}
