package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Project numbers («Projektnummer»): a Matas project whose intake issue has a Capex gets
// "<Capex number>-<n>", e.g. 1004-2, with n counting up within that Capex. The server assigns
// it the first time pf_capex is set on an intake without one, never an agent or a person. It
// never changes, also when the project changes Capex, and is never reused: the Capex register
// keeps the highest n used per Capex number, so a cancelled or deleted project's number stays
// taken. Both values live in metadata, so the schema is unchanged.

const (
	matasNumberKey        = "pf_number"
	matasNumberLastPrefix = "capex_number_last."
)

var (
	// A Capex number must fit in a metadata key, and the series is "<number>-<n>".
	matasCapexNumberRE   = regexp.MustCompile(`^[A-Za-z0-9_.]{1,40}$`)
	matasProjectNumberRE = regexp.MustCompile(`^([A-Za-z0-9_.]{1,40})-([1-9][0-9]{0,8})$`)
)

// matasNumberReserved is true for the keys only the server writes.
func matasNumberReserved(key string) bool {
	return key == matasNumberKey || strings.HasPrefix(key, matasNumberLastPrefix)
}

// matasNextNumber is the next n in a series: one past both the register's counter and every
// number still on an intake, so a lost or stale counter cannot hand out a number twice.
func matasNextNumber(capexNumber string, last int, existing []string) int {
	next := last
	for _, number := range existing {
		if m := matasProjectNumberRE.FindStringSubmatch(number); m != nil && m[1] == capexNumber {
			if n, _ := strconv.Atoi(m[2]); n > next {
				next = n
			}
		}
	}
	return next + 1
}

// matasAssignProjectNumber gives an intake issue its project number if it has a project and a
// Capex but no number yet. It returns the issue as stored afterwards; assigned reports whether
// this call wrote the number. Concurrent calls for the same Capex wait on one advisory lock.
func (h *Handler) matasAssignProjectNumber(ctx context.Context, issue db.Issue) (db.Issue, bool, error) {
	m := map[string]json.RawMessage{}
	_ = json.Unmarshal(issue.Metadata, &m)
	capexID := metaString(m, "pf_capex")
	if string(m["pf_intake"]) != "true" || !issue.ProjectID.Valid || capexID == "" || metaString(m, matasNumberKey) != "" {
		return issue, false, nil
	}
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return issue, false, err
	}
	defer tx.Rollback(ctx)

	register, catalog, err := matasCapexRegister(ctx, tx, issue.WorkspaceID)
	if err != nil {
		return issue, false, err
	}
	var capexNumber string
	for _, item := range catalog {
		if item["id"] == capexID {
			capexNumber, _ = item["number"].(string)
		}
	}
	if capexNumber == "" {
		return issue, false, fmt.Errorf("pf_capex %q is not in the Capex register", capexID)
	}
	if !matasCapexNumberRE.MatchString(capexNumber) {
		return issue, false, fmt.Errorf("Capex number %q cannot start a project number series", capexNumber)
	}
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", "capex_number/"+util.UUIDToString(issue.WorkspaceID)+"/"+capexNumber); err != nil {
		return issue, false, err
	}
	// Read again under the lock: another call may have numbered it, or the Capex changed.
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT metadata FROM issue WHERE id = $1 AND workspace_id = $2 FOR UPDATE`, issue.ID, issue.WorkspaceID).Scan(&raw); err != nil {
		return issue, false, err
	}
	m = map[string]json.RawMessage{}
	_ = json.Unmarshal(raw, &m)
	if metaString(m, matasNumberKey) != "" || metaString(m, "pf_capex") != capexID {
		return issue, false, nil
	}

	var registerMeta []byte
	if err := tx.QueryRow(ctx, `SELECT metadata FROM issue WHERE id = $1 FOR UPDATE`, register).Scan(&registerMeta); err != nil {
		return issue, false, err
	}
	rm := map[string]json.RawMessage{}
	_ = json.Unmarshal(registerMeta, &rm)
	lastKey := matasNumberLastPrefix + capexNumber
	var last int
	if v, ok := rm[lastKey]; ok && json.Unmarshal(v, &last) != nil {
		return issue, false, fmt.Errorf("register key %s is not a whole number", lastKey)
	}
	if _, present := rm[lastKey]; !present && len(rm) >= maxIssueMetadataKeys {
		return issue, false, fmt.Errorf("the Capex register has no room for %s", lastKey)
	}
	rows, err := tx.Query(ctx, `SELECT metadata->>'pf_number' FROM issue WHERE workspace_id = $1 AND metadata->>'pf_number' LIKE $2`, issue.WorkspaceID, capexNumber+"-%")
	if err != nil {
		return issue, false, err
	}
	existing, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return issue, false, err
	}
	n := matasNextNumber(capexNumber, last, existing)
	number := capexNumber + "-" + strconv.Itoa(n)

	qtx := h.Queries.WithTx(tx)
	if _, err := qtx.SetIssueMetadataKey(ctx, db.SetIssueMetadataKeyParams{ID: register, WorkspaceID: issue.WorkspaceID, Key: lastKey, Value: []byte(strconv.Itoa(n))}); err != nil {
		return issue, false, err
	}
	value, _ := json.Marshal(number)
	updated, err := qtx.SetIssueMetadataKey(ctx, db.SetIssueMetadataKeyParams{ID: issue.ID, WorkspaceID: issue.WorkspaceID, Key: matasNumberKey, Value: value})
	if err != nil {
		return issue, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return issue, false, err
	}
	return updated, true, nil
}

// matasCapexRegister finds the workspace's one Capex register and its catalog items.
func matasCapexRegister(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, ws pgtype.UUID) (pgtype.UUID, []map[string]any, error) {
	rows, err := q.Query(ctx, `SELECT id, metadata FROM issue WHERE workspace_id = $1 AND metadata @> '{"capex_register": true}'::jsonb LIMIT 2`, ws)
	if err != nil {
		return pgtype.UUID{}, nil, errors.New("could not read the Capex register")
	}
	defer rows.Close()
	var ids []pgtype.UUID
	var registers [][]byte
	for rows.Next() {
		var id pgtype.UUID
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return pgtype.UUID{}, nil, errors.New("could not read the Capex register")
		}
		ids, registers = append(ids, id), append(registers, raw)
	}
	if rows.Err() != nil || len(registers) != 1 {
		return pgtype.UUID{}, nil, errors.New("the workspace has no single Capex register")
	}
	m := map[string]json.RawMessage{}
	_ = json.Unmarshal(registers[0], &m)
	var catalog struct {
		Items []map[string]any `json:"items"`
	}
	if json.Unmarshal([]byte(metaString(m, "capex_catalog")), &catalog) != nil {
		return pgtype.UUID{}, nil, errors.New("the Capex register could not be read")
	}
	return ids[0], catalog.Items, nil
}

// matasProjectByNumber resolves a project number such as 1004-2 to its project.
func (h *Handler) matasProjectByNumber(ctx context.Context, ws pgtype.UUID, number string) (pgtype.UUID, error) {
	rows, err := h.DB.Query(ctx, `SELECT project_id FROM issue WHERE workspace_id = $1 AND project_id IS NOT NULL AND metadata @> '{"pf_intake": true}'::jsonb AND metadata->>'pf_number' = $2 LIMIT 2`, ws, number)
	if err != nil {
		return pgtype.UUID{}, errors.New("could not look up the project number")
	}
	found, err := pgx.CollectRows(rows, pgx.RowTo[pgtype.UUID])
	if err != nil {
		return pgtype.UUID{}, errors.New("could not look up the project number")
	}
	switch len(found) {
	case 0:
		return pgtype.UUID{}, fmt.Errorf("no project has project number %s; see multica_list_projects", number)
	case 1:
		return found[0], nil
	default:
		return pgtype.UUID{}, fmt.Errorf("project number %s is on more than one project; a person must fix that", number)
	}
}

// matasNumberOf is a project's number, or "" when it has none.
func (h *Handler) matasNumberOf(ctx context.Context, ws, project pgtype.UUID) string {
	var number pgtype.Text
	_ = h.DB.QueryRow(ctx, `SELECT metadata->>'pf_number' FROM issue WHERE workspace_id = $1 AND project_id = $2 AND metadata @> '{"pf_intake": true}'::jsonb AND metadata ? 'pf_number' ORDER BY created_at LIMIT 1`, ws, project).Scan(&number)
	return number.String
}

// matasNumbered writes a project's name the way titles and mails show it: «[1004-2] Name».
func matasNumbered(number, name string) string {
	if number == "" {
		return name
	}
	return "[" + number + "] " + name
}
