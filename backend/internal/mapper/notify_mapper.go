package mapper

import (
	"errors"
	"fmt"
	"opensync/internal/msg"
)

// GetNotifyList gets notify list, optionally only enabled ones
func GetNotifyList(needEnable bool) ([]map[string]interface{}, error) {
	var rows []map[string]interface{}
	var err error
	if needEnable {
		rows, err = FetchAllToTable("SELECT * FROM notify WHERE enable=1")
	} else {
		rows, err = FetchAllToTable("SELECT * FROM notify")
	}
	if err != nil {
		return nil, err
	}
	if err := decryptCredentialColumn(rows, "params"); err != nil {
		return nil, err
	}
	return rows, nil
}

// GetNotifyByID gets a single notify config by ID (raw params, internal use only).
func GetNotifyByID(notifyID int64) (map[string]interface{}, error) {
	rows, err := FetchAllToTable("SELECT * FROM notify WHERE id=?", notifyID)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	if err := decryptCredentialColumn(rows, "params"); err != nil {
		return nil, err
	}
	return rows[0], nil
}

// AddNotify inserts a new notify config
func AddNotify(notify map[string]interface{}) (int64, error) {
	params, err := encryptCredential(fmt.Sprintf("%v", notify["params"]))
	if err != nil {
		return 0, err
	}
	return ExecuteInsert(
		"INSERT INTO notify(enable, method, params) VALUES (?, ?, ?)",
		notify["enable"], notify["method"], params,
	)
}

// EditNotify updates a notify config. The recorded delivery outcome is cleared
// because it describes the previous configuration: keeping it would leave a
// just-corrected config showing the failure the user edited it to fix.
func EditNotify(notify map[string]interface{}) error {
	params, err := encryptCredential(fmt.Sprintf("%v", notify["params"]))
	if err != nil {
		return err
	}
	return executeNotifyUpdate(
		"UPDATE notify SET enable=?, method=?, params=?, lastSendStatus=0, lastSendTime=0, lastSendError=NULL WHERE id=?",
		notify["enable"], notify["method"], params, notify["id"],
	)
}

// UpdateNotifySendResult records the outcome of the most recent delivery for one
// config. A missing row is not an error: the config can be deleted while a
// queued notification is still in flight.
func UpdateNotifySendResult(notifyID int64, status int, sentAt int64, errMsg string) error {
	var storedErr interface{}
	if errMsg != "" {
		storedErr = errMsg
	}
	_, err := GetDB().Exec(
		"UPDATE notify SET lastSendStatus=?, lastSendTime=?, lastSendError=? WHERE id=?",
		status, sentAt, storedErr, notifyID,
	)
	return err
}

// UpdateNotifyStatus updates notify enable status
func UpdateNotifyStatus(notifyID int64, enable int) error {
	return executeNotifyUpdate("UPDATE notify SET enable=? WHERE id=?", enable, notifyID)
}

// DeleteNotify deletes a notify config
func DeleteNotify(notifyID int64) error {
	return executeNotifyUpdate("DELETE FROM notify WHERE id=?", notifyID)
}

func executeNotifyUpdate(query string, args ...interface{}) error {
	result, err := GetDB().Exec(query, args...)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return errors.New(msg.T(msg.NotifyNotFound))
	}
	return nil
}
