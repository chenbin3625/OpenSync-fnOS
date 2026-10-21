package mapper

import (
	"database/sql"
	"errors"
	"testing"

	"opensync/internal/config"
)

const testJobDDL = `CREATE TABLE job(
	id integer primary key autoincrement,
	enable integer DEFAULT 1,
	remark text,
	srcPath text,
	dstPath text,
	alistId integer,
	useCacheT integer DEFAULT 0,
	scanIntervalT integer DEFAULT 0,
	useCacheS integer DEFAULT 0,
	scanIntervalS integer DEFAULT 0,
	method integer,
	interval integer,
	isCron integer DEFAULT 0,
	month text DEFAULT NULL,
	day text DEFAULT NULL,
	day_of_week text DEFAULT NULL,
	hour text DEFAULT NULL,
	minute text DEFAULT NULL,
	second text DEFAULT NULL,
	exclude text DEFAULT NULL,
	minFileSize integer DEFAULT 0,
	maxFileSize integer DEFAULT 0,
	createTime integer DEFAULT (strftime('%s', 'now'))
)`

const testJobTaskDDL = `CREATE TABLE job_task(
	id integer primary key autoincrement,
	jobId integer,
	status integer DEFAULT 1,
	errMsg text,
	runTime integer,
	taskNum text,
	createTime integer DEFAULT (strftime('%s', 'now'))
)`

func createJobSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	mustExec(t, db, testJobDDL)
	mustExec(t, db, testJobTaskDDL)
	mustExec(t, db, testJobTaskItemDDL)
}

func jobTestMap(id int64) map[string]interface{} {
	job := map[string]interface{}{
		"enable": 1, "remark": "job", "srcPath": "/src", "dstPath": "/dst", "alistId": int64(1),
		"useCacheT": 1, "scanIntervalT": 10, "useCacheS": 0, "scanIntervalS": 0,
		"method": 0, "interval": 5, "isCron": 0,
		"month": nil, "day": nil, "day_of_week": nil, "hour": nil, "minute": nil, "second": nil,
		"exclude": nil, "minFileSize": 0, "maxFileSize": 0,
	}
	if id > 0 {
		job["id"] = id
	}
	return job
}

// P3-12: the job CRUD path had no tests at all; exercise it end to end against
// the real column set.
func TestJobCRUD(t *testing.T) {
	db := newFileTestDB(t)
	createJobSchema(t, db)
	restore := SetDBForTest(db)
	defer restore()

	id, err := AddJob(jobTestMap(0))
	if err != nil {
		t.Fatalf("AddJob() error = %v", err)
	}
	if id <= 0 {
		t.Fatalf("AddJob() id = %d", id)
	}
	job, err := GetJobByID(id)
	if err != nil {
		t.Fatalf("GetJobByID() error = %v", err)
	}
	if job["remark"] != "job" || job["srcPath"] != "/src" {
		t.Fatalf("GetJobByID() = %#v", job)
	}
	if _, err := GetJobByID(id + 100); !errors.Is(err, ErrJobNotFound) {
		t.Fatalf("GetJobByID(missing) error = %v, want ErrJobNotFound", err)
	}

	update := jobTestMap(id)
	update["remark"] = "renamed"
	update["interval"] = 11
	if err := UpdateJob(update); err != nil {
		t.Fatalf("UpdateJob() error = %v", err)
	}
	job, err = GetJobByID(id)
	if err != nil {
		t.Fatalf("GetJobByID() after update error = %v", err)
	}
	if job["remark"] != "renamed" || job["interval"] != int64(11) {
		t.Fatalf("job after update = %#v", job)
	}

	if err := UpdateJobEnable(id, 0); err != nil {
		t.Fatalf("UpdateJobEnable() error = %v", err)
	}
	job, err = GetJobByID(id)
	if err != nil || job["enable"] != int64(0) {
		t.Fatalf("enable after UpdateJobEnable = %#v, err = %v", job["enable"], err)
	}

	result, err := GetJobList(map[string]interface{}{})
	if err != nil {
		t.Fatalf("GetJobList() error = %v", err)
	}
	if result["count"] != int64(1) {
		t.Fatalf("GetJobList() count = %v, want 1", result["count"])
	}
	all, err := GetJobListAll()
	if err != nil || len(all) != 1 {
		t.Fatalf("GetJobListAll() = %d rows, %v", len(all), err)
	}
	enabled, err := GetEnableJobList()
	if err != nil {
		t.Fatalf("GetEnableJobList() error = %v", err)
	}
	if len(enabled) != 0 {
		t.Fatalf("GetEnableJobList() = %d rows after disabling, want 0", len(enabled))
	}
}

// P3-9: a nil/absent id used to match no row and still report success.
func TestUpdateJobRejectsMissingID(t *testing.T) {
	db := newFileTestDB(t)
	createJobSchema(t, db)
	restore := SetDBForTest(db)
	defer restore()

	job := jobTestMap(0)
	delete(job, "id")
	if err := UpdateJob(job); err == nil {
		t.Fatal("UpdateJob() without id = nil, want an error")
	}
}

func TestUpdateJobReportsMissingRow(t *testing.T) {
	db := newFileTestDB(t)
	createJobSchema(t, db)
	restore := SetDBForTest(db)
	defer restore()

	if err := UpdateJob(jobTestMap(999)); !errors.Is(err, ErrJobNotFound) {
		t.Fatalf("UpdateJob(unknown id) error = %v, want ErrJobNotFound", err)
	}
}

// The full-column update semantics are intentional (it is how optional fields
// are cleared); pin them so a future partial-map call cannot silently wipe
// settings without anyone noticing.
func TestUpdateJobTreatsMissingKeyAsNull(t *testing.T) {
	db := newFileTestDB(t)
	createJobSchema(t, db)
	restore := SetDBForTest(db)
	defer restore()

	id, err := AddJob(jobTestMap(0))
	if err != nil {
		t.Fatalf("AddJob() error = %v", err)
	}
	update := jobTestMap(id)
	delete(update, "remark")
	if err := UpdateJob(update); err != nil {
		t.Fatalf("UpdateJob() error = %v", err)
	}
	var remark sql.NullString
	if err := db.QueryRow("SELECT remark FROM job WHERE id=?", id).Scan(&remark); err != nil {
		t.Fatalf("read remark: %v", err)
	}
	if remark.Valid {
		t.Fatalf("remark = %q, want NULL: UpdateJob overwrites every column", remark.String)
	}
}

// P3-9/P3-10: progress is persisted when supplied (and NULL when absent), and
// RemoveAlist reports a missing row instead of a silent success.
func TestAddJobTaskItemManyPersistsOptionalProgress(t *testing.T) {
	db := newFileTestDB(t)
	createJobSchema(t, db)
	restore := SetDBForTest(db)
	defer restore()

	if err := AddJobTaskItemMany([]map[string]interface{}{
		{
			"taskId": int64(1), "srcPath": "/src/a", "dstPath": "/dst/a", "isPath": 0,
			"fileName": "a.bin", "fileSize": int64(10), "type": 0, "status": 2,
			"errMsg": nil, "progress": 42.5, "createTime": int64(100),
		},
		{
			"taskId": int64(1), "srcPath": "/src/b", "dstPath": "/dst/b", "isPath": 0,
			"fileName": "b.bin", "fileSize": int64(20), "type": 0, "status": 2,
			"errMsg": nil, "createTime": int64(101),
		},
	}); err != nil {
		t.Fatalf("AddJobTaskItemMany() error = %v", err)
	}
	var progress sql.NullFloat64
	if err := db.QueryRow("SELECT progress FROM job_task_item WHERE fileName='a.bin'").Scan(&progress); err != nil {
		t.Fatalf("read progress: %v", err)
	}
	if !progress.Valid || progress.Float64 != 42.5 {
		t.Fatalf("progress = %+v, want 42.5", progress)
	}
	if err := db.QueryRow("SELECT progress FROM job_task_item WHERE fileName='b.bin'").Scan(&progress); err != nil {
		t.Fatalf("read progress: %v", err)
	}
	if progress.Valid {
		t.Fatalf("progress = %v for an item without progress, want NULL", progress.Float64)
	}
}

func TestRemoveAlistReportsMissingRow(t *testing.T) {
	db := newFileTestDB(t)
	mustExec(t, db, `CREATE TABLE alist_list(id integer primary key autoincrement, remark text, url text, userName text, token text)`)
	restore := SetDBForTest(db)
	defer restore()
	setMapperTestConfig(t, &config.Config{Server: config.ServerConfig{PasswdStr: "alist-crud-test-key"}})

	id, err := AddAlist("remark", "https://alist.test", "user", "token")
	if err != nil {
		t.Fatalf("AddAlist() error = %v", err)
	}
	if err := RemoveAlist(id); err != nil {
		t.Fatalf("RemoveAlist(existing) error = %v", err)
	}
	if err := RemoveAlist(id); !errors.Is(err, ErrAlistNotFound) {
		t.Fatalf("RemoveAlist(missing) error = %v, want ErrAlistNotFound", err)
	}
}
