package account

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/store/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/schema"
)

// purgeOwnerColumns are the columns that tie a row to an account directly.
var purgeOwnerColumns = []string{"user_id", "owner_id", "recipient_id"}

// purgeUnownedModels lists the migrated tables without an owner column, each with how the purge treats
// it; a new table must either carry an owner column or be classified here, or the test fails.
var purgeUnownedModels = map[string]string{
	"app_user":              "the account row itself; asserted directly",
	"setting":               "global key/value table; per-user keys embed the user ID and are seeded explicitly",
	"alert_evaluator_state": "instance-wide evaluator clock, owned by no account",
	"memory_entry":          "scoped by tenant=user and subject=<user ID>; seeded explicitly",
	"memory_settings":       "scoped by tenant=user and subject=<user ID>; seeded explicitly",
}

type purgeForeignKey struct {
	columns    []string
	refTable   string
	refColumns []string
}

type purgeSeeder struct {
	t       *testing.T
	db      *gorm.DB
	cache   *sync.Map
	counter int
	// foreignKeys maps a table to its declared constraints; seeded keeps the string columns of the row
	// seeded per table and user, so a child row can reference its parent.
	foreignKeys map[string][]purgeForeignKey
	seeded      map[string]map[string]string
}

func newPurgeSeeder(t *testing.T, db *gorm.DB) *purgeSeeder {
	t.Helper()
	s := &purgeSeeder{t: t, db: db, cache: &sync.Map{}, foreignKeys: map[string][]purgeForeignKey{}, seeded: map[string]map[string]string{}}
	for _, m := range model.AllModels {
		parsed, err := schema.Parse(m, s.cache, db.NamingStrategy)
		if err != nil {
			t.Fatal(err)
		}
		for _, relation := range parsed.Relationships.Relations {
			constraint := relation.ParseConstraint()
			if constraint == nil {
				continue
			}
			fk := purgeForeignKey{refTable: constraint.ReferenceSchema.Table}
			for _, field := range constraint.ForeignKeys {
				fk.columns = append(fk.columns, field.DBName)
			}
			for _, field := range constraint.References {
				fk.refColumns = append(fk.refColumns, field.DBName)
			}
			table := constraint.Schema.Table
			s.foreignKeys[table] = append(s.foreignKeys[table], fk)
		}
	}
	return s
}

func (s *purgeSeeder) next() string {
	s.counter++
	return fmt.Sprintf("v%d", s.counter)
}

// seed inserts one row of the model with every owner column set to userID, every device column set to
// deviceID and every non-null foreign key pointing at the parent row seeded for the same user; the other
// columns get short values unique across the test. It reports false, inserting nothing, while a parent
// row is not seeded yet.
func (s *purgeSeeder) seed(m any, userID, deviceID string, overrides map[string]string) bool {
	s.t.Helper()
	parsed, err := schema.Parse(m, s.cache, s.db.NamingStrategy)
	if err != nil {
		s.t.Fatal(err)
	}
	resolved := map[string]string{}
	for column, value := range overrides {
		resolved[column] = value
	}
	for _, fk := range s.foreignKeys[parsed.Table] {
		if fk.refTable == "app_user" || fk.refTable == parsed.Table {
			continue
		}
		if fk.refTable == "user_device" && len(fk.columns) == 1 && fk.columns[0] == "device_id" {
			continue
		}
		if field := parsed.LookUpField(fk.columns[0]); field != nil && field.FieldType.Kind() == reflect.Pointer {
			continue
		}
		parent, ok := s.seeded[fk.refTable+"|"+userID]
		if !ok {
			return false
		}
		for i, column := range fk.columns {
			if _, set := resolved[column]; !set {
				resolved[column] = parent[fk.refColumns[i]]
			}
		}
	}
	overrides = resolved
	row := reflect.New(parsed.ModelType)
	value := row.Elem()
	for _, field := range parsed.Fields {
		if field.DBName == "" || field.AutoIncrement || field.PrimaryKey && field.DataType == schema.Int {
			continue
		}
		target := field.ReflectValueOf(context.Background(), value)
		text := s.next()
		if override, ok := overrides[field.DBName]; ok {
			text = override
		} else if field.DBName == "device_id" {
			text = deviceID
		} else if contains(purgeOwnerColumns, field.DBName) {
			text = userID
		}
		kind := target.Kind()
		if kind == reflect.Pointer {
			if field.DBName != "device_id" && !contains(purgeOwnerColumns, field.DBName) {
				continue
			}
			target.Set(reflect.New(target.Type().Elem()))
			target = target.Elem()
			kind = target.Kind()
		}
		switch kind {
		case reflect.String:
			target.SetString(text)
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			target.SetInt(1)
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			target.SetUint(1)
		case reflect.Float32, reflect.Float64:
			target.SetFloat(1)
		case reflect.Bool:
			target.SetBool(false)
		case reflect.Slice:
			if target.Type().Elem().Kind() == reflect.Uint8 {
				target.SetBytes([]byte{1})
			}
		default:
			s.t.Fatalf("%s.%s: unsupported seed kind %s", parsed.Table, field.DBName, kind)
		}
	}
	if err := s.db.Omit(clause.Associations).Create(row.Interface()).Error; err != nil {
		s.t.Fatalf("seed %s: %v", parsed.Table, err)
	}
	columns := map[string]string{}
	for _, field := range parsed.Fields {
		if field.DBName == "" {
			continue
		}
		fieldValue := field.ReflectValueOf(context.Background(), value)
		if fieldValue.Kind() == reflect.String {
			columns[field.DBName] = fieldValue.String()
		}
	}
	s.seeded[parsed.Table+"|"+userID] = columns
	return true
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func countWhere(t *testing.T, db *gorm.DB, table, query string, args ...any) int64 {
	t.Helper()
	var count int64
	if err := db.Table(table).Where(query, args...).Count(&count).Error; err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return count
}

func TestDatabasePurgeUserRemovesEveryOwnedRow(t *testing.T) {
	st := dbtest.NewFixture(t).OpenStore(t)
	db := st.GORM()
	a := New(db)
	ctx := context.Background()

	superadmin, err := a.CreateUser(ctx, "purge-admin", "", "synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&store.AppUser{}).Where("id = ?", superadmin.ID).Update("role", string(RoleSuperadmin)).Error; err != nil {
		t.Fatal(err)
	}
	target, err := a.CreateUser(ctx, "purge-target", "", "synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	keeper, err := a.CreateUser(ctx, "purge-keeper", "", "synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}

	seeder := newPurgeSeeder(t, db)
	devices := map[string]string{target.ID: ids.New(), keeper.ID: ids.New()}
	type classified struct {
		table   string
		columns []string
	}
	var ownerTables []classified
	var deviceTables []string
	for _, userID := range []string{target.ID, keeper.ID} {
		seeder.seed(&model.UserDevice{}, userID, devices[userID], map[string]string{"id": devices[userID]})
	}
	var pending []any
	for _, m := range model.AllModels {
		parsed, err := schema.Parse(m, seeder.cache, db.NamingStrategy)
		if err != nil {
			t.Fatal(err)
		}
		var columns []string
		hasDevice := false
		for _, field := range parsed.Fields {
			if contains(purgeOwnerColumns, field.DBName) {
				columns = append(columns, field.DBName)
			}
			if field.DBName == "device_id" {
				hasDevice = true
			}
		}
		switch {
		case len(columns) > 0:
			ownerTables = append(ownerTables, classified{table: parsed.Table, columns: columns})
		case hasDevice:
			deviceTables = append(deviceTables, parsed.Table)
		default:
			if _, ok := purgeUnownedModels[parsed.Table]; !ok {
				t.Fatalf("table %s has no user_id/owner_id/recipient_id/device_id column and is not classified in purgeUnownedModels; extend PurgeUser and the classification", parsed.Table)
			}
			continue
		}
		if parsed.Table != "user_device" {
			pending = append(pending, m)
		}
	}
	for len(pending) > 0 {
		var blocked []any
		for _, m := range pending {
			if !seeder.seed(m, target.ID, devices[target.ID], nil) {
				blocked = append(blocked, m)
				continue
			}
			seeder.seed(m, keeper.ID, devices[keeper.ID], nil)
		}
		if len(blocked) == len(pending) {
			t.Fatalf("cannot seed %d tables: their parent rows never become available", len(blocked))
		}
		pending = blocked
	}
	// A share the keeper grants to the target is removed from the recipient side.
	seeder.seed(&model.HostShare{}, keeper.ID, devices[keeper.ID], map[string]string{"recipient_id": target.ID})
	// The keeper's own audit record that mentions the target stays.
	if err := db.Create(&model.AuditRow{Source: "auth", Kind: "admin_delete_user", PayloadJSON: `{"target_user_id":"` + target.ID + `"}`, OwnerID: keeper.ID}).Error; err != nil {
		t.Fatal(err)
	}
	for _, userID := range []string{target.ID, keeper.ID} {
		for _, m := range []any{&model.MemoryEntry{}, &model.MemorySettings{}} {
			seeder.seed(m, userID, devices[userID], map[string]string{"tenant": memoryUserTenant, "subject": userID})
		}
	}
	settingKeys := func(userID string) []string {
		return []string{
			"ai.permission.user." + userID,
			"ai.device_permission.user." + userID + ".clocks",
			"user.ai.models." + userID,
			"user.layout." + userID,
			"sync.cursor." + userID,
			"sync.kh_tombstone." + userID + ".example.org",
		}
	}
	for _, userID := range []string{target.ID, keeper.ID} {
		for _, key := range settingKeys(userID) {
			if err := db.Create(&model.Setting{Key: key, Value: "1", UpdatedAt: 1}).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := db.Create(&model.Setting{Key: "ai.permission", Value: "1", UpdatedAt: 1}).Error; err != nil {
		t.Fatal(err)
	}

	if _, err := a.BeginDeleteUser(ctx, target.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.PurgeUser(ctx, target.ID); err != nil {
		t.Fatal(err)
	}
	// A second sweep over an already purged account is a no-op.
	if err := a.PurgeUser(ctx, target.ID); err != nil {
		t.Fatal(err)
	}

	for _, table := range ownerTables {
		for _, column := range table.columns {
			if got := countWhere(t, db, table.table, column+" = ?", target.ID); got != 0 {
				t.Errorf("%s keeps %d rows with %s = deleted user", table.table, got, column)
			}
		}
		want := int64(1)
		if table.table == "audit_log" {
			want = 2
		}
		if got := countWhere(t, db, table.table, table.columns[0]+" = ?", keeper.ID); got != want {
			t.Errorf("%s keeps %d rows of the other user, want %d", table.table, got, want)
		}
	}
	for _, table := range deviceTables {
		if got := countWhere(t, db, table, "device_id = ?", devices[target.ID]); got != 0 {
			t.Errorf("%s keeps %d rows of the deleted user's device", table, got)
		}
		if got := countWhere(t, db, table, "device_id = ?", devices[keeper.ID]); got != 1 {
			t.Errorf("%s keeps %d rows of the other user's device, want 1", table, got)
		}
	}
	for _, table := range []string{"memory_entry", "memory_settings"} {
		if got := countWhere(t, db, table, "tenant = ? AND subject = ?", memoryUserTenant, target.ID); got != 0 {
			t.Errorf("%s keeps %d rows of the deleted user", table, got)
		}
		if got := countWhere(t, db, table, "tenant = ? AND subject = ?", memoryUserTenant, keeper.ID); got != 1 {
			t.Errorf("%s keeps %d rows of the other user, want 1", table, got)
		}
	}
	var remaining []string
	if err := db.Model(&model.Setting{}).Pluck("key", &remaining).Error; err != nil {
		t.Fatal(err)
	}
	wantKept := append(settingKeys(keeper.ID), "ai.permission")
	for _, key := range remaining {
		if strings.Contains(key, target.ID) {
			t.Errorf("setting %s survived the purge", key)
		}
	}
	for _, key := range wantKept {
		if !contains(remaining, key) {
			t.Errorf("setting %s of another scope was removed", key)
		}
	}
	if got := countWhere(t, db, "app_user", "id = ?", target.ID); got != 0 {
		t.Fatalf("deleted account row remains")
	}
	for _, id := range []string{superadmin.ID, keeper.ID} {
		if got := countWhere(t, db, "app_user", "id = ?", id); got != 1 {
			t.Fatalf("account %s was removed", id)
		}
	}
	if _, err := a.GetUser(ctx, target.ID); err == nil {
		t.Fatal("deleted account still resolves")
	}
	// A recreated username gets a fresh ID and inherits nothing.
	recreated, err := a.CreateUser(ctx, "purge-target", "", "synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	if recreated.ID == target.ID {
		t.Fatal("recreated username reused the deleted user ID")
	}
}

func TestDatabaseBeginDeleteUserGuards(t *testing.T) {
	a, _ := testDatabaseAccounts(t)
	ctx := context.Background()
	if _, err := a.BeginDeleteUser(ctx, ids.New()); err == nil {
		t.Fatal("unknown user accepted")
	} else {
		requireCode(t, err, ipc.CodeNotFound)
	}
	only, err := a.CreateUser(ctx, "only-admin", "", "synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.db.Model(&store.AppUser{}).Where("id = ?", only.ID).Update("role", string(RoleSuperadmin)).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := a.BeginDeleteUser(ctx, only.ID); err == nil {
		t.Fatal("last active superadmin accepted")
	} else {
		requireCode(t, err, ipc.CodeBadParam)
	}
	if user, err := a.GetUser(ctx, only.ID); err != nil || user.State != StateActive {
		t.Fatalf("refused delete changed the account: %+v, %v", user, err)
	}

	second, err := a.CreateUser(ctx, "second-admin", "", "synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.db.Model(&store.AppUser{}).Where("id = ?", second.ID).Update("role", string(RoleSuperadmin)).Error; err != nil {
		t.Fatal(err)
	}
	token, _, err := a.IssueSessionWithMetadata(ctx, second.ID, "", SessionMetadata{})
	if err != nil {
		t.Fatal(err)
	}
	epoch := a.Epoch(second.ID)
	if _, err := a.BeginDeleteUser(ctx, second.ID); err != nil {
		t.Fatal(err)
	}
	requireCode(t, validateErr(a, ctx, token), ipc.CodeForbidden)
	if a.CheckEpoch(second.ID, epoch) {
		t.Fatal("begin delete did not advance the account epoch")
	}
	if user, err := a.GetUser(ctx, second.ID); err != nil || user.State != StateDisabled {
		t.Fatalf("begin delete left the account usable: %+v, %v", user, err)
	}
}

func TestSettingKeyNamesUser(t *testing.T) {
	id := "01JABCDEFGHJKMNPQRSTVWXYZ0"
	for _, key := range []string{"user.layout." + id, "ai.permission.user." + id + ".clocks", "sync.kh_tombstone." + id + ".host"} {
		if !settingKeyNamesUser(key, id) {
			t.Errorf("%s not matched", key)
		}
	}
	for _, key := range []string{"user.layout.X" + id, "user.layout." + id + "Z", "ai.permission"} {
		if settingKeyNamesUser(key, id) {
			t.Errorf("%s matched", key)
		}
	}
}

// The blocked listeners (the fleet service kicks the account's device agents) look devices up by user, so they
// must run after the disable commits and before the purge removes the device rows.
func TestDatabaseBeginDeleteUserNotifiesWhileDevicesExist(t *testing.T) {
	a, _ := testDatabaseAccounts(t)
	ctx := context.Background()
	target, err := a.CreateUser(ctx, "device-owner", "", "synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.db.Create(&store.UserDevice{ID: ids.New(), UserID: target.ID, Name: "laptop", Kind: "desktop", CreatedAt: 1}).Error; err != nil {
		t.Fatal(err)
	}
	type observation struct {
		devices int64
		state   string
		err     error
	}
	observed := make(chan observation, 1)
	a.OnUserBlocked(func(userID string) {
		if userID != target.ID {
			return
		}
		var result observation
		result.err = a.db.Model(&store.UserDevice{}).Where("user_id = ?", userID).Count(&result.devices).Error
		if result.err == nil {
			result.err = a.db.Model(&store.AppUser{}).Where("id = ?", userID).Pluck("state", &result.state).Error
		}
		observed <- result
	})
	if _, err := a.BeginDeleteUser(ctx, target.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-observed:
		if result.err != nil || result.devices != 1 || result.state != string(StateDisabled) {
			t.Fatalf("listener saw devices=%d state=%q err=%v, want the device row of a disabled account", result.devices, result.state, result.err)
		}
	default:
		t.Fatal("blocked listener did not run")
	}
	if err := a.PurgeUser(ctx, target.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-observed:
		t.Fatal("purge notified the blocked listeners again after the device rows were gone")
	default:
	}
}

func TestDatabasePurgeUserRejectsNonAccountIDs(t *testing.T) {
	a, _ := testDatabaseAccounts(t)
	ctx := context.Background()
	user, err := a.CreateUser(ctx, "settings-owner", "", "synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	key := "ai.permission.user." + user.ID
	if err := a.db.Create(&model.Setting{Key: key, Value: "1", UpdatedAt: 1}).Error; err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"", "user", "permission", "%"} {
		requireCode(t, a.PurgeUser(ctx, id), ipc.CodeBadParam)
		if _, err := a.BeginDeleteUser(ctx, id); err == nil {
			t.Fatalf("begin delete accepted %q", id)
		} else {
			requireCode(t, err, ipc.CodeNotFound)
		}
	}
	var count int64
	if err := a.db.Model(&model.Setting{}).Where(&model.Setting{Key: key}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("per-user setting after rejected purges = %d, %v", count, err)
	}
}
