package output

import (
	"encoding/json"

	"github.com/TsekNet/fleet-plan/internal/diff"
)

// JSONDiffOutput is the structured JSON output for AI agents and CI.
type JSONDiffOutput struct {
	Teams []JSONTeamDiff `json:"teams"`
}

// JSONTeamDiff is a single team's diff in JSON format.
type JSONTeamDiff struct {
	Team     string             `json:"team"`
	Policies JSONResourceDiff   `json:"policies"`
	Queries  JSONResourceDiff   `json:"queries"`
	Software JSONResourceDiff   `json:"software"`
	Profiles JSONResourceDiff   `json:"profiles"`
	Scripts  JSONResourceDiff   `json:"scripts"`
	Labels   JSONLabelResult    `json:"labels"`
	Config   []JSONConfigChange `json:"config,omitempty"`
	Errors   []string           `json:"errors"`
}

// JSONConfigChange is a config change in JSON format.
type JSONConfigChange struct {
	Section string `json:"section"`
	Key     string `json:"key"`
	Old     string `json:"old,omitempty"`
	New     string `json:"new"`
}

// JSONResourceDiff is a resource diff in JSON format.
type JSONResourceDiff struct {
	Added    []JSONChange `json:"added"`
	Modified []JSONChange `json:"modified"`
	Deleted  []JSONChange `json:"deleted"`
}

// JSONChange is a single change in JSON format.
type JSONChange struct {
	Name      string               `json:"name"`
	Fields    map[string]JSONField `json:"fields,omitempty"`
	HostCount uint                 `json:"host_count,omitempty"`
	Warning   string               `json:"warning,omitempty"`
}

// JSONField is an old/new field value in JSON format.
// Old/New are string for scalar fields or []string for slice fields (e.g. categories).
type JSONField struct {
	Old any `json:"old"`
	New any `json:"new"`
}

// JSONLabelResult is label validation in JSON format.
type JSONLabelResult struct {
	Valid   []JSONLabel `json:"valid"`
	Missing []JSONLabel `json:"missing"`
}

// JSONLabel is a label reference in JSON format.
type JSONLabel struct {
	Name         string `json:"name"`
	HostCount    uint   `json:"host_count,omitempty"`
	ReferencedBy string `json:"referenced_by,omitempty"`
}

// RenderDiffJSON renders diff results as structured JSON.
func RenderDiffJSON(results []diff.DiffResult) (string, error) {
	output := JSONDiffOutput{
		Teams: make([]JSONTeamDiff, 0, len(results)),
	}

	for _, r := range results {
		teamDiff := JSONTeamDiff{
			Team:     r.Team,
			Policies: convertResourceDiff(r.Policies),
			Queries:  convertResourceDiff(r.Queries),
			Software: convertResourceDiff(r.Software),
			Profiles: convertResourceDiff(r.Profiles),
			Scripts:  convertResourceDiff(r.Scripts),
			Labels:   convertLabels(r.Labels),
			Config:   convertConfigChanges(r.Config),
			Errors:   r.Errors,
		}
		if teamDiff.Errors == nil {
			teamDiff.Errors = []string{}
		}
		output.Teams = append(output.Teams, teamDiff)
	}

	data, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func convertResourceDiff(rd diff.ResourceDiff) JSONResourceDiff {
	return JSONResourceDiff{
		Added:    convertChanges(rd.Added),
		Modified: convertChanges(rd.Modified),
		Deleted:  convertChanges(rd.Deleted),
	}
}

func convertChanges(changes []diff.ResourceChange) []JSONChange {
	result := make([]JSONChange, 0, len(changes))
	for _, c := range changes {
		jc := JSONChange{
			Name:      c.Name,
			HostCount: c.HostCount,
			Warning:   c.Warning,
		}
		if len(c.Fields) > 0 {
			jc.Fields = make(map[string]JSONField)
			for k, v := range c.Fields {
				if v.IsSlice {
					var old, new any
					if v.OldSlice != nil {
						old = v.OldSlice
					}
					if v.NewSlice != nil {
						new = v.NewSlice
					}
					jc.Fields[k] = JSONField{Old: old, New: new}
				} else {
					jc.Fields[k] = JSONField{Old: v.Old, New: v.New}
				}
			}
		}
		result = append(result, jc)
	}
	return result
}

func convertConfigChanges(changes []diff.ConfigChange) []JSONConfigChange {
	if len(changes) == 0 {
		return nil
	}
	result := make([]JSONConfigChange, 0, len(changes))
	for _, c := range changes {
		result = append(result, JSONConfigChange{
			Section: c.Section,
			Key:     c.Key,
			Old:     c.Old,
			New:     c.New,
		})
	}
	return result
}

func convertLabels(lv diff.LabelValidation) JSONLabelResult {
	result := JSONLabelResult{
		Valid:   make([]JSONLabel, 0, len(lv.Valid)),
		Missing: make([]JSONLabel, 0, len(lv.Missing)),
	}
	for _, l := range lv.Valid {
		result.Valid = append(result.Valid, JSONLabel{
			Name:         l.Name,
			HostCount:    l.HostCount,
			ReferencedBy: l.ReferencedBy,
		})
	}
	for _, l := range lv.Missing {
		result.Missing = append(result.Missing, JSONLabel{
			Name:         l.Name,
			ReferencedBy: l.ReferencedBy,
		})
	}
	return result
}
