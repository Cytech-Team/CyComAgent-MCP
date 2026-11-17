package federation

import "testing"

func TestRemoteJobCatalogRequiresExactAnnotations(t *testing.T) {
	cases := []struct {
		name       string
		annotation any
		wantErr    bool
	}{
		{"federation_remote_job_reserve", false, false},
		{"federation_remote_job_reserve", true, true},
		{"federation_remote_job_status", true, false},
		{"federation_remote_job_status", false, true},
		{"federation_remote_job_reconcile", true, false},
		{"federation_remote_job_reconcile", "true", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateRemoteJobCatalog([]Tool{{Name: tc.name, Annotations: map[string]any{"readOnlyHint": tc.annotation}}}, tc.name)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error=%v wantErr=%v", err, tc.wantErr)
			}
		})
	}
	if err := validateRemoteJobCatalog(nil, "federation_remote_job_status"); err == nil {
		t.Fatal("missing tool was accepted")
	}
	if err := validateRemoteJobCatalog(nil, "federation_remote_job_other"); err == nil {
		t.Fatal("unknown tool was accepted")
	}
}
