package fault

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		class   string
		params  map[string]string
		wantErr string
		want    Spec
	}{
		{
			name:   "config.missing-key with a covered key",
			class:  "config.missing-key",
			params: map[string]string{"configMap": "smtp-config", "key": "SMTP_PORT"},
			want:   Spec{Class: ClassConfigMissingKey, ConfigMap: "smtp-config", Key: "SMTP_PORT"},
		},
		{
			name:    "unknown class enumerates the catalog",
			class:   "config.typo",
			wantErr: "accepted classes: config.missing-key",
		},
		{
			name:    "missing parameter",
			class:   "config.missing-key",
			params:  map[string]string{"configMap": "smtp-config"},
			wantErr: `parameter "key" is required`,
		},
		{
			name:    "unknown parameter",
			class:   "config.missing-key",
			params:  map[string]string{"configMap": "c", "key": "SMTP_PORT", "mode": "x"},
			wantErr: `unknown parameter "mode"`,
		},
		{
			name:    "a key the application does not validate is refused",
			class:   "config.missing-key",
			params:  map[string]string{"configMap": "smtp-config", "key": "SMTP_USER"},
			wantErr: `key "SMTP_USER" is not one ` + AppImage + ` validates`,
		},
		{
			name:   "config.unbounded-cache with a covered setting",
			class:  "config.unbounded-cache",
			params: map[string]string{"env": "CACHE_MAX_ENTRIES"},
			want:   Spec{Class: ClassConfigUnboundedCache, Key: "CACHE_MAX_ENTRIES"},
		},
		{
			name:    "a setting the application does not read as a bound is refused",
			class:   "config.unbounded-cache",
			params:  map[string]string{"env": "CACHE_TTL"},
			wantErr: `setting "CACHE_TTL" is not one ` + AppImage + ` reads as a cache bound`,
		},
		{
			name:    "config.unbounded-cache requires its setting",
			class:   "config.unbounded-cache",
			params:  map[string]string{},
			wantErr: `parameter "env" is required`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := Parse(tc.class, tc.params)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestCatalogIsStructural(t *testing.T) {
	t.Parallel()
	cat := Catalog()
	if len(cat) != len(Classes()) {
		t.Fatalf("Catalog and Classes disagree: %d vs %d", len(cat), len(Classes()))
	}
	for class, def := range cat {
		if def.Class != class {
			t.Errorf("catalog key %q does not match definition class %q", class, def.Class)
		}
		if def.Symptom == "" {
			t.Errorf("%s declares no symptom", class)
		}
		if len(def.Params) == 0 {
			t.Errorf("%s declares no parameters", class)
		}
	}
}

// fakePods is a PodLister returning a scripted sequence of pod lists.
type fakePods struct {
	lists [][]podJSON
	calls int
}

type podJSON struct {
	name       string
	labels     map[string]string
	deleting   bool
	restarts   int
	waiting    string
	termExit   *int
	lastExit   *int
	termReason string // defaults to Error
	lastReason string // defaults to Error
}

func reasonOr(r string) string {
	if r == "" {
		return "Error"
	}
	return r
}

func intp(i int) *int { return &i }

func (f *fakePods) ListResources(_ context.Context, kind, _ string) (string, error) {
	if kind != "pods" {
		return "", errors.New("unexpected kind " + kind)
	}
	idx := f.calls
	if idx >= len(f.lists) {
		idx = len(f.lists) - 1
	}
	f.calls++
	items := make([]map[string]any, 0, len(f.lists[idx]))
	for _, p := range f.lists[idx] {
		meta := map[string]any{"name": p.name, "labels": p.labels}
		if p.deleting {
			meta["deletionTimestamp"] = "2026-08-21T00:00:00Z"
		}
		state := map[string]any{}
		if p.waiting != "" {
			state["waiting"] = map[string]any{"reason": p.waiting}
		}
		if p.termExit != nil {
			state["terminated"] = map[string]any{"reason": reasonOr(p.termReason), "exitCode": *p.termExit}
		}
		last := map[string]any{}
		if p.lastExit != nil {
			last["terminated"] = map[string]any{"reason": reasonOr(p.lastReason), "exitCode": *p.lastExit}
		}
		items = append(items, map[string]any{
			"metadata": meta,
			"status": map[string]any{
				"phase": "Running",
				"containerStatuses": []map[string]any{{
					"restartCount": p.restarts, "state": state, "lastState": last,
				}},
			},
		})
	}
	b, err := json.Marshal(map[string]any{"items": items})
	return string(b), err
}

func TestWaitForSymptom_CrashLoopMatchesRestartAfterFailure(t *testing.T) {
	t.Parallel()
	sel := map[string]string{"app": "n"}
	f := &fakePods{lists: [][]podJSON{
		// First poll: fresh pods, no restart yet.
		{{name: "a", labels: sel}, {name: "b", labels: sel}},
		// Second: a restarted after exit 1 (terminated state), b still waiting in backoff.
		{{name: "a", labels: sel, restarts: 1, termExit: intp(1)}, {name: "b", labels: sel, restarts: 1, waiting: "CrashLoopBackOff"}},
	}}
	err := WaitForSymptom(context.Background(), f, "default", sel, 2, Expect{Status: "CrashLoopBackOff"}, 10*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.calls != 2 {
		t.Fatalf("expected 2 polls, got %d", f.calls)
	}
}

func TestWaitForSymptom_TimeoutReportsObserved(t *testing.T) {
	t.Parallel()
	sel := map[string]string{"app": "n"}
	f := &fakePods{lists: [][]podJSON{
		{{name: "a", labels: sel, waiting: "CreateContainerConfigError"}, {name: "other", labels: map[string]string{"app": "x"}, waiting: "CrashLoopBackOff"}},
	}}
	err := WaitForSymptom(context.Background(), f, "default", sel, 1, Expect{Status: "CrashLoopBackOff"}, 0)
	var unmet *ErrSymptomUnmet
	if !errors.As(err, &unmet) {
		t.Fatalf("want *ErrSymptomUnmet, got %v", err)
	}
	if got := unmet.Error(); !strings.Contains(got, "a=CreateContainerConfigError(restarts=0)") || strings.Contains(got, "other") {
		t.Fatalf("unexpected message: %s", got)
	}
}

func TestWaitForSymptom_RequiresMinPods(t *testing.T) {
	t.Parallel()
	sel := map[string]string{"app": "n"}
	f := &fakePods{lists: [][]podJSON{
		{{name: "a", labels: sel, restarts: 1, lastExit: intp(1)}},
	}}
	err := WaitForSymptom(context.Background(), f, "default", sel, 2, Expect{Status: "CrashLoopBackOff"}, 0)
	if err == nil {
		t.Fatal("expected timeout with one of two pods present")
	}
}

func TestWaitForSymptom_IgnoresTerminatingPods(t *testing.T) {
	t.Parallel()
	sel := map[string]string{"app": "n"}
	f := &fakePods{lists: [][]podJSON{
		{{name: "old", labels: sel, deleting: true}, {name: "new", labels: sel, restarts: 2, waiting: "CrashLoopBackOff"}},
	}}
	if err := WaitForSymptom(context.Background(), f, "default", sel, 1, Expect{Status: "crashloopbackoff"}, 0); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWaitForSymptom_GenericStatusMatchesReason(t *testing.T) {
	t.Parallel()
	sel := map[string]string{"app": "n"}
	f := &fakePods{lists: [][]podJSON{
		{{name: "a", labels: sel, waiting: "CreateContainerConfigError"}},
	}}
	if err := WaitForSymptom(context.Background(), f, "default", sel, 1, Expect{Status: "CreateContainerConfigError"}, 0); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWaitForSymptom_OOMKilledWithRestartMinimum(t *testing.T) {
	t.Parallel()
	sel := map[string]string{"app": "u"}
	f := &fakePods{lists: [][]podJSON{
		// Running before the first kill.
		{{name: "a", labels: sel}},
		// Killed once: the reason is there, the restart count is not yet.
		{{name: "a", labels: sel, restarts: 1, lastExit: intp(137), lastReason: "OOMKilled"}},
		// Waiting out its backoff after the second kill.
		{{name: "a", labels: sel, restarts: 2, termExit: intp(137), termReason: "OOMKilled", lastExit: intp(137), lastReason: "OOMKilled"}},
	}}
	err := WaitForSymptom(context.Background(), f, "default", sel, 1, Expect{Status: "OOMKilled", Restarts: 2}, 10*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.calls != 3 {
		t.Fatalf("expected 3 polls, got %d", f.calls)
	}
}

func TestWaitForSymptom_RestartMinimumUnmetIsReported(t *testing.T) {
	t.Parallel()
	sel := map[string]string{"app": "u"}
	f := &fakePods{lists: [][]podJSON{
		{{name: "a", labels: sel, restarts: 1, lastExit: intp(137), lastReason: "OOMKilled"}},
	}}
	err := WaitForSymptom(context.Background(), f, "default", sel, 1, Expect{Status: "OOMKilled", Restarts: 3}, 0)
	var unmet *ErrSymptomUnmet
	if !errors.As(err, &unmet) {
		t.Fatalf("want *ErrSymptomUnmet, got %v", err)
	}
	if got := unmet.Error(); !strings.Contains(got, `"OOMKilled" with restarts>=3`) || !strings.Contains(got, "a=OOMKilled(restarts=1)") {
		t.Fatalf("unexpected message: %s", got)
	}
}

func TestWaitForSymptom_OOMKilledDoesNotMatchCrashLoop(t *testing.T) {
	t.Parallel()
	sel := map[string]string{"app": "u"}
	f := &fakePods{lists: [][]podJSON{
		{{name: "a", labels: sel, restarts: 3, waiting: "CrashLoopBackOff", lastExit: intp(1)}},
	}}
	if err := WaitForSymptom(context.Background(), f, "default", sel, 1, Expect{Status: "OOMKilled"}, 0); err == nil {
		t.Fatal("a crash loop on exit 1 is not an OOM kill")
	}
}

// fakeInjectClient scripts the runtime trigger's cluster surface.
type fakeInjectClient struct {
	*fakePods
	deployment string
	cm         map[string]string
	deleted    []string
	restarted  []string
	setEnv     []string
}

func (f *fakeInjectClient) SetDeploymentEnv(_ context.Context, ns, name, key, value string) error {
	f.setEnv = append(f.setEnv, ns+"/"+name+"/"+key+"="+value)
	return nil
}

func (f *fakeInjectClient) GetResource(_ context.Context, kind, _, _ string) (string, error) {
	if kind != "deployment" {
		return "", errors.New("unexpected kind " + kind)
	}
	return f.deployment, nil
}

func (f *fakeInjectClient) GetConfigMap(_ context.Context, _, _ string) (map[string]string, error) {
	return f.cm, nil
}

func (f *fakeInjectClient) DeleteConfigMapKey(_ context.Context, ns, name, key string) error {
	f.deleted = append(f.deleted, ns+"/"+name+"/"+key)
	delete(f.cm, key)
	return nil
}

func (f *fakeInjectClient) RestartDeployment(_ context.Context, ns, name string) error {
	f.restarted = append(f.restarted, ns+"/"+name)
	return nil
}

func appDeployment(image, cmName string) string {
	return `{"spec":{"replicas":2,"selector":{"matchLabels":{"app":"n"}},"template":{"spec":{"containers":[{"image":"` + image +
		`","env":[{"name":"SMTP_PORT","valueFrom":{"configMapKeyRef":{"name":"` + cmName + `","key":"SMTP_PORT"}}}]}]}}}}`
}

func TestInject_RemovesKeyRestartsAndWaitsOnNewGeneration(t *testing.T) {
	t.Parallel()
	sel := map[string]string{"app": "n"}
	pods := &fakePods{lists: [][]podJSON{
		// Snapshot before the restart: two healthy pods.
		{{name: "old-1", labels: sel}, {name: "old-2", labels: sel}},
		// After: the old ones still healthy, one new pod crash-looping.
		{{name: "old-1", labels: sel}, {name: "old-2", labels: sel}, {name: "new-1", labels: sel, restarts: 1, termExit: intp(1)}},
	}}
	kube := &fakeInjectClient{fakePods: pods, deployment: appDeployment(AppImage, "smtp-config"), cm: map[string]string{"SMTP_HOST": "h", "SMTP_PORT": "587"}}
	spec, err := Parse("config.missing-key", map[string]string{"configMap": "smtp-config", "key": "SMTP_PORT"})
	if err != nil {
		t.Fatal(err)
	}
	if err := Inject(context.Background(), kube, Target{Namespace: "default", Name: "n"}, spec, Expect{}, 5*time.Second); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(kube.deleted) != 1 || kube.deleted[0] != "default/smtp-config/SMTP_PORT" {
		t.Fatalf("deleted = %v", kube.deleted)
	}
	if len(kube.restarted) != 1 || kube.restarted[0] != "default/n" {
		t.Fatalf("restarted = %v", kube.restarted)
	}
}

func TestInject_Refusals(t *testing.T) {
	t.Parallel()
	sel := map[string]string{"app": "n"}
	spec := Spec{Class: ClassConfigMissingKey, ConfigMap: "smtp-config", Key: "SMTP_PORT"}
	tests := []struct {
		name    string
		kube    *fakeInjectClient
		wantErr string
	}{
		{
			name:    "target not on the application image",
			kube:    &fakeInjectClient{fakePods: &fakePods{lists: [][]podJSON{{}}}, deployment: appDeployment("registry.k8s.io/nginx-slim:0.27", "smtp-config")},
			wantErr: "does not run the application image",
		},
		{
			name:    "target does not read the ConfigMap",
			kube:    &fakeInjectClient{fakePods: &fakePods{lists: [][]podJSON{{}}}, deployment: appDeployment(AppImage, "other")},
			wantErr: `does not read ConfigMap "smtp-config"`,
		},
		{
			name:    "key already absent",
			kube:    &fakeInjectClient{fakePods: &fakePods{lists: [][]podJSON{{{name: "a", labels: sel}}}}, deployment: appDeployment(AppImage, "smtp-config"), cm: map[string]string{"SMTP_HOST": "h"}},
			wantErr: `has no key "SMTP_PORT" to remove`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := Inject(context.Background(), tc.kube, Target{Namespace: "default", Name: "n"}, spec, Expect{}, time.Second)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
			if len(tc.kube.deleted) != 0 || len(tc.kube.restarted) != 0 {
				t.Fatalf("refusal must not mutate: deleted=%v restarted=%v", tc.kube.deleted, tc.kube.restarted)
			}
		})
	}
}

func cacheDeployment(image, limit, cacheValue string) string {
	resources := `{}`
	if limit != "" {
		resources = `{"limits":{"memory":"` + limit + `"}}`
	}
	env := `[]`
	if cacheValue != "" {
		env = `[{"name":"CACHE_MAX_ENTRIES","value":"` + cacheValue + `"}]`
	}
	return `{"spec":{"replicas":1,"selector":{"matchLabels":{"app":"u"}},"template":{"spec":{"containers":[{"image":"` + image +
		`","resources":` + resources + `,"env":` + env + `}]}}}}`
}

func TestInject_UnboundedCacheSetsEnvAndWaitsForOOMKill(t *testing.T) {
	t.Parallel()
	sel := map[string]string{"app": "u"}
	pods := &fakePods{lists: [][]podJSON{
		{{name: "old-1", labels: sel}},
		{{name: "old-1", labels: sel}, {name: "new-1", labels: sel, restarts: 1, lastExit: intp(137), lastReason: "OOMKilled"}},
	}}
	kube := &fakeInjectClient{fakePods: pods, deployment: cacheDeployment(AppImage, "64Mi", "")}
	spec, err := Parse("config.unbounded-cache", map[string]string{"env": "CACHE_MAX_ENTRIES"})
	if err != nil {
		t.Fatal(err)
	}
	if err := Inject(context.Background(), kube, Target{Namespace: "default", Name: "u"}, spec, Expect{}, 5*time.Second); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(kube.setEnv) != 1 || kube.setEnv[0] != "default/u/CACHE_MAX_ENTRIES=0" {
		t.Fatalf("setEnv = %v", kube.setEnv)
	}
	if len(kube.restarted) != 0 {
		t.Fatalf("the env change is the rollout; no separate restart expected, got %v", kube.restarted)
	}
}

func TestInject_UnboundedCacheRefusals(t *testing.T) {
	t.Parallel()
	spec := Spec{Class: ClassConfigUnboundedCache, Key: "CACHE_MAX_ENTRIES"}
	tests := []struct {
		name       string
		deployment string
		wantErr    string
	}{
		{name: "no memory limit", deployment: cacheDeployment(AppImage, "", "1024"), wantErr: "sets no memory limit"},
		{name: "already unbounded", deployment: cacheDeployment(AppImage, "64Mi", "0"), wantErr: "already sets CACHE_MAX_ENTRIES=0"},
		{name: "not the application", deployment: cacheDeployment("registry.k8s.io/nginx-slim:0.27", "64Mi", ""), wantErr: "does not run the application image"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			kube := &fakeInjectClient{fakePods: &fakePods{lists: [][]podJSON{{}}}, deployment: tc.deployment}
			err := Inject(context.Background(), kube, Target{Namespace: "default", Name: "u"}, spec, Expect{}, time.Second)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
			if len(kube.setEnv) != 0 {
				t.Fatalf("refusal must not mutate: setEnv=%v", kube.setEnv)
			}
		})
	}
}
