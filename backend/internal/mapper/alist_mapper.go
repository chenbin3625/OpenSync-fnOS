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
	encryptedToken, err := encryptCredential(token)
	if err != nil {
		return 0, err
	}
	return ExecuteInsert(
		"INSERT INTO alist_list (remark, url, userName, token) VALUES (?, ?, ?, ?)",
		remark, url, userName, encryptedToken,
	)
}

// UpdateAlist updates an alist entry
func UpdateAlist(id int64, remark, url string, token *string) error {
	if token != nil {
		encryptedToken, err := encryptCredential(*token)
		if err != nil {
			return err
		}
		return ExecuteUpdate("UPDATE alist_list SET remark=?, url=?, token=? WHERE id=?", remark, url, encryptedToken, id)
	}
	return ExecuteUpdate("UPDATE alist_list SET remark=?, url=? WHERE id=?", remark, url, id)
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
