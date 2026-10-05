package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

// Fleet returns label scoping as objects; fleet-plan needs the names.

func TestGetPoliciesLabelScoping(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"policies":[{"id":1,"name":"P",
			"labels_include_any":[{"name":"pilots","id":7}],
			"labels_exclude_any":[{"name":"exempt","id":9}]}]}`))
	}))
	defer ts.Close()

	policies, err := testClient(t, ts, "tok").GetPolicies(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(policies[0].LabelsIncludeAny, []string{"pilots"}) {
		t.Errorf("LabelsIncludeAny = %v", policies[0].LabelsIncludeAny)
	}
	if !slices.Equal(policies[0].LabelsExcludeAny, []string{"exempt"}) {
		t.Errorf("LabelsExcludeAny = %v", policies[0].LabelsExcludeAny)
	}
}

func TestGetProfilesLabelScoping(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"profiles":[{"profile_uuid":"u","name":"DOR","platform":"darwin",
			"labels_include_all":[{"name":"a","id":1}],
			"labels_include_any":[{"name":"b","id":2}],
			"labels_exclude_any":[{"name":"exempt","id":3}]}],"meta":{"has_next_results":false}}`))
	}))
	defer ts.Close()

	profiles, err := testClient(t, ts, "tok").GetProfiles(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	p := profiles[0]
	if !slices.Equal(p.LabelsIncludeAll, []string{"a"}) || !slices.Equal(p.LabelsIncludeAny, []string{"b"}) ||
		!slices.Equal(p.LabelsExcludeAny, []string{"exempt"}) {
		t.Errorf("labels = all:%v any:%v exclude:%v", p.LabelsIncludeAll, p.LabelsIncludeAny, p.LabelsExcludeAny)
	}
}

func TestGetLabelsDefinitionFields(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"labels":[{"id":1,"name":"x","description":"d",
			"label_membership_type":"manual","label_type":"regular"}]}`))
	}))
	defer ts.Close()

	labels, err := testClient(t, ts, "tok").GetLabels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	l := labels[0]
	if l.Description != "d" || l.LabelMembershipType != "manual" || l.LabelType != "regular" {
		t.Errorf("label = %+v", l)
	}
}
