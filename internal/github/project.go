package github

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Project field names the bot depends on (README: "GitHub setup").
const (
	FieldStatus   = "Status"
	FieldPriority = "Priority"
	FieldDueDate  = "Due date"
	FieldEstimate = "Estimate"
)

var (
	statusOptions   = []string{"Todo", "In progress", "In review", "Blocked", "Done"}
	priorityOptions = []string{"High", "Medium", "Low"}
)

// Field is one Project (v2) field; Options maps option name → id.
type Field struct {
	ID       string
	Name     string
	DataType string
	Options  map[string]string
}

// Schema is a Project's id plus the ids of the fields the bot uses.
type Schema struct {
	ProjectID string
	Title     string
	Status    Field
	Priority  Field
	DueDate   Field
	Estimate  *Field // optional
}

// ErrSchema means the Project is missing a field or option; the message
// says exactly what to fix.
var ErrSchema = errors.New("github project not set up as required")

const fieldsQuery = `query($org: String!, $number: Int!) {
  organization(login: $org) {
    projectV2(number: $number) {
      id
      title
      fields(first: 50) {
        nodes {
          ... on ProjectV2FieldCommon { id name dataType }
          ... on ProjectV2SingleSelectField { options { id name } }
        }
      }
    }
  }
}`

type rawField struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	DataType string `json:"dataType"`
	Options  []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"options"`
}

// FetchSchema reads the Project's fields and resolves the ones the bot
// needs by name. A missing field or option returns an ErrSchema error.
func (a *App) FetchSchema(ctx context.Context, org string, number int) (*Schema, error) {
	var out struct {
		Organization *struct {
			ProjectV2 *struct {
				ID     string `json:"id"`
				Title  string `json:"title"`
				Fields struct {
					Nodes []rawField `json:"nodes"`
				} `json:"fields"`
			} `json:"projectV2"`
		} `json:"organization"`
	}
	err := a.GraphQL(ctx, org, fieldsQuery, map[string]any{"org": org, "number": number}, &out, false)
	if err != nil {
		var ge *GraphQLError
		if errors.As(err, &ge) {
			return nil, fmt.Errorf("%w: org %q Project #%d: %v (does the Project exist, and does the App have Organization projects access?)",
				ErrSchema, org, number, err)
		}
		return nil, err
	}
	if out.Organization == nil || out.Organization.ProjectV2 == nil {
		return nil, fmt.Errorf("%w: org %q has no Project #%d visible to the App", ErrSchema, org, number)
	}
	p := out.Organization.ProjectV2
	s, err := parseSchema(p.Fields.Nodes)
	if err != nil {
		return nil, fmt.Errorf("%w: org %q Project #%d %q: %v", ErrSchema, org, number, p.Title, err)
	}
	s.ProjectID, s.Title = p.ID, p.Title
	return s, nil
}

// parseSchema resolves the required fields by exact name.
func parseSchema(nodes []rawField) (*Schema, error) {
	byName := map[string]rawField{}
	var names []string
	for _, n := range nodes {
		if n.ID == "" {
			continue // a field type that doesn't implement ProjectV2FieldCommon
		}
		byName[n.Name] = n
		names = append(names, n.Name)
	}
	sort.Strings(names)

	var problems []string
	get := func(name, dataType string, options []string) Field {
		raw, ok := byName[name]
		if !ok {
			problems = append(problems, fmt.Sprintf("missing field %q (%s)", name, strings.ToLower(strings.ReplaceAll(dataType, "_", " "))))
			return Field{}
		}
		if raw.DataType != dataType {
			problems = append(problems, fmt.Sprintf("field %q is %s, must be %s", name, raw.DataType, dataType))
			return Field{}
		}
		f := Field{ID: raw.ID, Name: raw.Name, DataType: raw.DataType, Options: map[string]string{}}
		for _, o := range raw.Options {
			f.Options[o.Name] = o.ID
		}
		for _, want := range options {
			if _, ok := f.Options[want]; !ok {
				problems = append(problems, fmt.Sprintf("field %q has no option %q", name, want))
			}
		}
		return f
	}

	s := &Schema{
		Status:   get(FieldStatus, "SINGLE_SELECT", statusOptions),
		Priority: get(FieldPriority, "SINGLE_SELECT", priorityOptions),
		DueDate:  get(FieldDueDate, "DATE", nil),
	}
	if raw, ok := byName[FieldEstimate]; ok && raw.DataType == "NUMBER" {
		s.Estimate = &Field{ID: raw.ID, Name: raw.Name, DataType: raw.DataType}
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("%s (fields found: %s)", strings.Join(problems, "; "), strings.Join(names, ", "))
	}
	return s, nil
}
