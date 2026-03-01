package types

import "testing"

func TestCompanyValidate(t *testing.T) {
	validLevels := map[int]LevelSpec{
		1: {Clusters: 1, TTLDefaultHours: 4},
	}

	tests := []struct {
		name    string
		company Company
		wantErr bool
	}{
		{
			name: "valid company",
			company: Company{
				Name:          "acme",
				CloudProvider: CloudProviderAWS,
				IaCTool:       IaCToolTerraform,
				Levels:        validLevels,
			},
			wantErr: false,
		},
		{
			name:    "missing name",
			company: Company{CloudProvider: CloudProviderAWS, IaCTool: IaCToolTerraform, Levels: validLevels},
			wantErr: true,
		},
		{
			name:    "missing cloud provider",
			company: Company{Name: "acme", IaCTool: IaCToolTerraform, Levels: validLevels},
			wantErr: true,
		},
		{
			name:    "missing iac tool",
			company: Company{Name: "acme", CloudProvider: CloudProviderAWS, Levels: validLevels},
			wantErr: true,
		},
		{
			name:    "no levels",
			company: Company{Name: "acme", CloudProvider: CloudProviderAWS, IaCTool: IaCToolTerraform},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.company.Validate()
			if (err != nil) != tc.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestCompanyGetLevel(t *testing.T) {
	c := Company{
		Name: "acme",
		Levels: map[int]LevelSpec{
			1: {Clusters: 1},
			2: {Clusters: 2},
		},
	}

	tests := []struct {
		name         string
		level        int
		wantClusters int
		wantErr      bool
	}{
		{"defined level 1", 1, 1, false},
		{"defined level 2", 2, 2, false},
		{"undefined level 3", 3, 0, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			spec, err := c.GetLevel(tc.level)
			if (err != nil) != tc.wantErr {
				t.Fatalf("GetLevel(%d) error = %v, wantErr %v", tc.level, err, tc.wantErr)
			}
			if !tc.wantErr && spec.Clusters != tc.wantClusters {
				t.Errorf("GetLevel(%d).Clusters = %d, want %d", tc.level, spec.Clusters, tc.wantClusters)
			}
		})
	}
}
