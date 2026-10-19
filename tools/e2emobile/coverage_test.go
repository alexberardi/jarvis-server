package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

// navDir is a src/navigation like the mobile repo's: a root stack holding a tab navigator and
// a modal stack, multi-line registrations with an icon's name= in their options, and a screen
// name (Detail) used by two navigators.
func navDir(t *testing.T) string {
	d := filepath.Join(t.TempDir(), "src", "navigation")
	write(t, filepath.Join(d, "RootNavigator.tsx"), `import MainTabNavigator from './MainTabNavigator';
import InboxStackNavigator from './InboxStackNavigator';
const Stack = createNativeStackNavigator<RootStackParamList>();
const RootNavigator = () => {
  if (mustChangePassword) {
    return <ForcePasswordChangeScreen />;
  }
  return (
    <Stack.Navigator screenOptions={{ headerShown: false }}>
      <Stack.Screen name="Main" component={MainTabNavigator} />
      <Stack.Screen
        name="Inbox"
        component={InboxStackNavigator}
        options={{ presentation: 'modal' }}
      />
      <Stack.Screen
        name="Settings"
        component={SettingsScreen}
        options={{ presentation: 'modal' }}
      />
    </Stack.Navigator>
  );
};
`)
	write(t, filepath.Join(d, "MainTabNavigator.tsx"), `const Tab = createBottomTabNavigator<MainTabParamList>();
const MainTabNavigator = () => (
    <Tab.Navigator>
      <Tab.Screen
        name="HomeTab"
        component={HomeScreen}
        options={{
          tabBarIcon: ({ color, size }) => (
            <MaterialCommunityIcons name="home" color={color} size={size} />
          ),
        }}
      />
      <Tab.Screen name="Detail" component={DetailScreen} />
    </Tab.Navigator>
);
`)
	write(t, filepath.Join(d, "InboxStackNavigator.tsx"), `const Stack = createNativeStackNavigator<InboxStackParamList>();
export default () => (
  <Stack.Navigator>
    <Stack.Screen name="InboxList" component={InboxListScreen} />
    <Stack.Screen name="Detail" component={InboxDetailScreen} />
  </Stack.Navigator>
);
`)
	write(t, filepath.Join(d, "types.ts"), "export type RootStackParamList = { Main: undefined };\n")
	return d
}

func TestScreens(t *testing.T) {
	got, err := Screens(navDir(t))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"InboxStackNavigator/Detail", "InboxStackNavigator/InboxList", "MainTabNavigator/Detail",
		"MainTabNavigator/HomeTab", "RootNavigator/Settings"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestScreensWithoutRegistrationsIsAnError(t *testing.T) {
	if _, err := Screens(t.TempDir()); err == nil {
		t.Fatal("an empty navigation dir must not pass as zero screens")
	}
}

func TestCheckCoverage(t *testing.T) {
	flows := flowsDir(t)
	screens := []string{"A/Covered", "A/Manual", "A/Pending", "A/Missing", "A/NoFlow", "A/Both", "A/Tests"}
	entries := map[string]Entry{
		"A/Covered": {Flows: []string{".maestro/login-admin.yaml", ".maestro/subflows/login.yaml"}},
		"A/Manual":  {Manual: "needs a camera"},
		"A/Pending": {Pending: "#145"},
		"A/NoFlow":  {Flows: []string{".maestro/gone.yaml"}},
		"A/Both":    {Flows: []string{".maestro/login-admin.yaml"}, Manual: "x"},
		"A/Tests":   {Tests: []string{"e2e/web/tests/chat.spec.ts"}},
		"A/Removed": {Manual: "was a screen once"},
	}
	r := CheckCoverage(entries, screens, discover(t, flows), flows, ".maestro")
	if r.OK() {
		t.Fatal("OK with problems")
	}
	check := func(name string, got, want []string) {
		t.Helper()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %q, want %q", name, got, want)
		}
	}
	check("covered", r.Covered, []string{"A/Covered"})
	check("manual", r.Manual, []string{"A/Manual: needs a camera"})
	check("pending", r.Pending, []string{"A/Pending: #145"})
	check("missing", r.Missing, []string{"A/Missing"})
	check("stale", r.Stale, []string{"A/Removed"})
	// chat-fail.yaml and bare.yml are listed by no entry (biometric-login.yaml is skipped).
	if len(r.Invalid) != 5 || !strings.Contains(strings.Join(r.Invalid, "\n"), "no flow .maestro/gone.yaml") {
		t.Errorf("invalid: %q", r.Invalid)
	}
	p := r.Problems()
	for _, s := range []string{"screen A/Missing", "A/Removed, which src/navigation does not register", "A/Both: want exactly one"} {
		if !strings.Contains(p, s) {
			t.Errorf("problems lack %q:\n%s", s, p)
		}
	}

	ok := CheckCoverage(map[string]Entry{"A/Covered": {Flows: []string{".maestro/login-admin.yaml", ".maestro/chat-fail.yaml",
		".maestro/bare.yml"}}}, []string{"A/Covered"}, discover(t, flows), flows, ".maestro")
	if !ok.OK() || ok.Problems() != "" {
		t.Errorf("clean: %+v", ok)
	}
}

func discover(t *testing.T, dir string) []Flow {
	t.Helper()
	flows, err := Discover(dir, ".maestro", nil)
	if err != nil {
		t.Fatal(err)
	}
	return flows
}

// A pending entry that names a flow which exists points at merged work: the gate fails until
// the entry lists it under flows. Free text and a flow still to be written pass.
func TestCheckCoveragePendingNamingAnExistingFlow(t *testing.T) {
	dir := flowsDir(t)
	all := Entry{Flows: []string{".maestro/login-admin.yaml", ".maestro/chat-fail.yaml", ".maestro/bare.yml"}}
	for _, tc := range []struct {
		pending string
		invalid string // "" when the entry stays pending
	}{
		{"#145", ""},
		{"#145: payments.yaml (not written yet)", ""},
		{"#145: chat-fail.yaml", "A/Pending: pending names .maestro/chat-fail.yaml, which exists: list it under flows"},
		{"#145: .maestro/login-admin.yaml, subflows/login.yaml", "A/Pending: pending names .maestro/login-admin.yaml, .maestro/subflows/login.yaml, which exist: list them under flows"},
		{"#145: biometric-login.yaml", "A/Pending: pending names .maestro/biometric-login.yaml, which exists: list it under flows"},
	} {
		r := CheckCoverage(map[string]Entry{"A/All": all, "A/Pending": {Pending: tc.pending}}, []string{"A/All", "A/Pending"},
			discover(t, dir), dir, ".maestro")
		if tc.invalid == "" {
			if !r.OK() || len(r.Pending) != 1 {
				t.Errorf("%q: %+v", tc.pending, r)
			}
			continue
		}
		if r.OK() || len(r.Pending) != 0 || len(r.Invalid) != 1 || r.Invalid[0] != tc.invalid {
			t.Errorf("%q: invalid %q, pending %q; want %q", tc.pending, r.Invalid, r.Pending, tc.invalid)
		}
	}
}

// Every flow the suite runs covers some screen: a flow no entry lists is merged work the map
// has not caught up with. Skipped flows and subflows need no entry.
func TestCheckCoverageEveryRunFlowIsListed(t *testing.T) {
	dir := flowsDir(t)
	r := CheckCoverage(map[string]Entry{"A/Home": {Flows: []string{".maestro/login-admin.yaml", ".maestro/subflows/login.yaml"}},
		"A/Chat": {Pending: "#145"}}, []string{"A/Home", "A/Chat"}, discover(t, dir), dir, ".maestro")
	want := []string{
		"flow .maestro/bare.yml covers no screen: list it under the flows of the screens it reaches",
		"flow .maestro/chat-fail.yaml covers no screen: list it under the flows of the screens it reaches",
	}
	if !reflect.DeepEqual(r.Invalid, want) {
		t.Errorf("invalid: got %q\nwant %q", r.Invalid, want)
	}
	if !strings.Contains(r.Problems(), "e2e/coverage.json: flow .maestro/bare.yml covers no screen") {
		t.Errorf("problems:\n%s", r.Problems())
	}
}

// writeCoverage is an e2e/coverage.json for navDir's screens; drop leaves one out.
func writeCoverage(t *testing.T, drop string) string {
	mobile := map[string]Entry{
		"InboxStackNavigator/Detail":    {Pending: "#145"},
		"InboxStackNavigator/InboxList": {Flows: []string{".maestro/login-admin.yaml"}},
		"MainTabNavigator/Detail":       {Manual: "needs a camera"},
		"MainTabNavigator/HomeTab":      {Flows: []string{".maestro/chat-fail.yaml", ".maestro/bare.yml"}},
		"RootNavigator/Settings":        {Pending: "#153"},
	}
	delete(mobile, drop)
	raw, _ := json.Marshal(Coverage{Web: map[string]Entry{"/": {Tests: []string{"x"}}}, Mobile: mobile})
	p := filepath.Join(t.TempDir(), "coverage.json")
	write(t, p, string(raw))
	return p
}

func TestRunChecksCoverageFirst(t *testing.T) {
	maestro, adb, _ := fakes(t)
	nav := navDir(t)
	for _, tc := range []struct {
		drop, status string
	}{{"", "passed"}, {"RootNavigator/Settings", "failed"}} {
		out := filepath.Join(t.TempDir(), "mobile")
		c := &config{flowsDir: flowsDir(t), rel: ".maestro", out: out, maestro: maestro, adb: adb, serial: "emulator-5580",
			coverage: writeCoverage(t, tc.drop), nav: nav, flowTimeout: time.Minute, adbTimeout: 10 * time.Second}
		flows, err := Discover(c.flowsDir, c.rel, regexp.MustCompile("^login-admin")) // a flow that passes
		if err != nil || len(flows) != 1 {
			t.Fatal(flows, err)
		}
		res, err := run(context.Background(), c, flows)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Tests) != 2 || res.Tests[0].ID != "coverage" || res.Tests[1].Status != "passed" {
			t.Fatalf("%s: tests %+v", tc.drop, res.Tests)
		}
		cov := res.Tests[0]
		if cov.Status != tc.status || res.Status != tc.status {
			t.Errorf("drop %q: coverage %s, suite %s, want %s", tc.drop, cov.Status, res.Status, tc.status)
		}
		if tc.drop != "" && (cov.Error == nil || !strings.Contains(*cov.Error, "screen RootNavigator/Settings")) {
			t.Errorf("the failure names the screen: %+v", cov.Error)
		}
		var file CoverageResult
		raw, err := os.ReadFile(filepath.Join(out, "coverage.json"))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &file); err != nil || file.Screens != 5 || len(file.Covered) != 2 {
			t.Errorf("coverage.json: %+v %v", file, err)
		}
	}
}

func TestRunWithABrokenCoverageFileFails(t *testing.T) {
	maestro, adb, _ := fakes(t)
	c := &config{flowsDir: flowsDir(t), rel: ".maestro", out: filepath.Join(t.TempDir(), "mobile"), maestro: maestro, adb: adb,
		serial: "emulator-5580", coverage: filepath.Join(t.TempDir(), "none.json"), nav: navDir(t),
		flowTimeout: time.Minute, adbTimeout: 10 * time.Second}
	flows, _ := Discover(c.flowsDir, c.rel, regexp.MustCompile("^login-admin"))
	res, err := run(context.Background(), c, flows)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "failed" || res.Tests[0].Status != "failed" || res.Tests[0].Error == nil {
		t.Fatalf("%+v", res.Tests[0])
	}
}

func TestParseConfigCoverageFlags(t *testing.T) {
	if _, _, err := parseConfig("list", []string{"--flows", "d", "--coverage", "c.json"}); err == nil {
		t.Error("--coverage without --nav")
	}
	if _, _, err := parseConfig("coverage", []string{"--flows", "d"}); err == nil {
		t.Error("coverage without --coverage")
	}
	if _, _, err := parseConfig("coverage", []string{"--flows", "d", "--coverage", "c.json", "--nav", "n"}); err != nil {
		t.Error(err)
	}
}
