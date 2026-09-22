package mapper

import (
	"errors"
	"opensync/internal/msg"
	"opensync/pkg/util"
)

// GetAlistList gets all alist entries
func GetAlistList() ([]map[string]interface{}, error) {
	rows, err := FetchAllToTable("SELECT * FROM alist_list")
	if err != nil {
		return nil, err
	}
	if err := decryptCredentialColumn(rows, "token"); err != nil {
		return nil, err
	}
	return rows, nil
}

// GetAlistByID gets alist by ID
func GetAlistByID(alistID int64) (map[string]interface{}, error) {
	rst, err := FetchAllToTable("SELECT * FROM alist_list WHERE id=?", alistID)
	if err != nil {
		return nil, err
	}
	if len(rst) == 0 {
		return nil, errors.New(msg.T(msg.AlistNotFound))
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

// RemoveAlist deletes an alist entry
func RemoveAlist(alistID int64) error {
	return ExecuteUpdate("DELETE FROM alist_list WHERE id=?", alistID)
}
