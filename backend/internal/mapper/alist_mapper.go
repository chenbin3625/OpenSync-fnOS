package mapper

import (
	"opensync/internal/msg"
	"opensync/pkg/util"
)

// GetAlistList gets all alist entries
func GetAlistList() ([]map[string]interface{}, error) {
	rows, err := FetchAllToTable("SELECT * FROM alist_list")
	if err != nil {
		return nil, err
	}
	decryptCredentialColumnLenient(rows, "alist_list", "token")
	return rows, nil
}

// GetAlistByID gets alist by ID. A token that cannot be decrypted fails with
// ErrCredentialUnreadable rather than returning a blank token, so an edit
// cannot mistake it for "keep the stored token" and a client is never built
// with an empty credential.
func GetAlistByID(alistID int64) (map[string]interface{}, error) {
	rst, err := FetchAllToTable("SELECT * FROM alist_list WHERE id=?", alistID)
	if err != nil {
		return nil, err
	}
	if len(rst) == 0 {
		return nil, notFound(ErrAlistNotFound, msg.AlistNotFound)
	}
	if err := decryptCredentialColumn(rst, "token"); err != nil {
		return nil, err
	}
	return rst[0], nil
}

// CountJobsByAlistID counts jobs that still reference an AList engine.
func CountJobsByAlistID(alistID int64) (int64, error) {
	val, err := FetchFirstVal("SELECT COUNT(*) FROM job WHERE alistId=?", alistID)
	if err != nil {
		return 0, err
	}
	return util.ToInt64(val), nil
}

// AddAlist inserts a new alist entry
func AddAlist(remark, url, userName, token string) (int64, error) {
	return AddAlistWithConcurrency(remark, url, userName, token, 0)
}

// AddAlistWithConcurrency inserts a new alist entry with an optional per-engine
// maxConcurrentJobs cap when the schema supports it.
func AddAlistWithConcurrency(remark, url, userName, token string, maxConcurrentJobs int) (int64, error) {
	encryptedToken, err := encryptCredential(token)
	if err != nil {
		return 0, err
	}
	hasCol, _ := tableHasColumnDBErr(GetDB(), "alist_list", "maxConcurrentJobs")
	var id int64
	if hasCol {
		id, err = ExecuteInsert(
			"INSERT INTO alist_list (remark, url, userName, token, maxConcurrentJobs) VALUES (?, ?, ?, ?, ?)",
			remark, url, userName, encryptedToken, maxConcurrentJobs,
		)
	} else {
		id, err = ExecuteInsert(
			"INSERT INTO alist_list (remark, url, userName, token) VALUES (?, ?, ?, ?)",
			remark, url, userName, encryptedToken,
		)
	}
	if isUniqueConstraintError(err) {
		return 0, alistExistsError()
	}
	return id, err
}

// UpdateAlist updates an alist entry
func UpdateAlist(id int64, remark, url string, token *string) error {
	return UpdateAlistWithConcurrency(id, remark, url, token, nil)
}

// UpdateAlistWithConcurrency updates an alist entry and optionally its
// maxConcurrentJobs limit when non-nil and supported by the schema.
func UpdateAlistWithConcurrency(id int64, remark, url string, token *string, maxConcurrentJobs *int) error {
	var (
		affected int64
		err      error
	)
	hasCol := false
	if maxConcurrentJobs != nil {
		hasCol, _ = tableHasColumnDBErr(GetDB(), "alist_list", "maxConcurrentJobs")
	}
	if token != nil {
		encryptedToken, encErr := encryptCredential(*token)
		if encErr != nil {
			return encErr
		}
		if hasCol {
			affected, err = executeUpdateAffected("UPDATE alist_list SET remark=?, url=?, token=?, maxConcurrentJobs=? WHERE id=?", remark, url, encryptedToken, *maxConcurrentJobs, id)
		} else {
			affected, err = executeUpdateAffected("UPDATE alist_list SET remark=?, url=?, token=? WHERE id=?", remark, url, encryptedToken, id)
		}
	} else {
		if hasCol {
			affected, err = executeUpdateAffected("UPDATE alist_list SET remark=?, url=?, maxConcurrentJobs=? WHERE id=?", remark, url, *maxConcurrentJobs, id)
		} else {
			affected, err = executeUpdateAffected("UPDATE alist_list SET remark=?, url=? WHERE id=?", remark, url, id)
		}
	}
	if isUniqueConstraintError(err) {
		return alistExistsError()
	}
	if err != nil {
		return err
	}
	if affected == 0 {
		return notFound(ErrAlistNotFound, msg.AlistNotFound)
	}
	return nil
}

// RemoveAlist deletes an alist entry. Deleting a row that does not exist is
// reported as not-found instead of a silent success, so a caller can tell a
// double delete (or a concurrently removed engine) from a real one.
func RemoveAlist(alistID int64) error {
	affected, err := executeUpdateAffected("DELETE FROM alist_list WHERE id=?", alistID)
	if err != nil {
		return err
	}
	if affected == 0 {
		return notFound(ErrAlistNotFound, msg.AlistNotFound)
	}
	return nil
}
