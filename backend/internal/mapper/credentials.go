package mapper

import (
	"database/sql"
	"fmt"
	"opensync/internal/config"
	appcrypto "opensync/pkg/crypto"
)

type credentialTarget struct {
	table, column string
}

// allowedCredentialTargets is the exhaustive set of (table, column) pairs
// that may appear in the migration loop. These are compile-time constants,
// NOT user input. Any future addition must be listed here.
var allowedCredentialTargets = map[credentialTarget]struct{}{
	{table: "alist_list", column: "token"}: {},
	{table: "notify", column: "params"}:    {},
}

func encryptCredential(value string) (string, error) {
	return appcrypto.EncryptString(value, config.GetConfig().Server.PasswdStr)
}

func decryptCredential(value interface{}) (string, error) {
	plain, _, err := appcrypto.DecryptString(fmt.Sprintf("%v", value), config.GetConfig().Server.PasswdStr)
	return plain, err
}

func decryptCredentialColumn(rows []map[string]interface{}, column string) error {
	for _, row := range rows {
		value, ok := row[column]
		if !ok || value == nil {
			continue
		}
		plain, err := decryptCredential(value)
		if err != nil {
			return err
		}
		row[column] = plain
	}
	return nil
}

func migrateStoredCredentials(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	for _, target := range []struct {
		table, column string
	}{
		{table: "alist_list", column: "token"},
		{table: "notify", column: "params"},
	} {
		if _, ok := allowedCredentialTargets[credentialTarget{table: target.table, column: target.column}]; !ok {
			return fmt.Errorf("credential migration: unknown target %s.%s", target.table, target.column)
		}
		rows, err := tx.Query(fmt.Sprintf("SELECT id, %s FROM %s", target.column, target.table))
		if err != nil {
			return err
		}
		values := make(map[int64]string)
		for rows.Next() {
			var id int64
			var value sql.NullString
			if err := rows.Scan(&id, &value); err != nil {
				rows.Close()
				return err
			}
			if value.Valid && value.String != "" {
				values[id] = value.String
			}
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for id, value := range values {
			_, encrypted, err := appcrypto.DecryptString(value, config.GetConfig().Server.PasswdStr)
			if err != nil {
				return fmt.Errorf("validate encrypted %s.%s row %d: %w", target.table, target.column, id, err)
			}
			if encrypted {
				continue
			}
			ciphertext, err := encryptCredential(value)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(
				fmt.Sprintf("UPDATE %s SET %s=? WHERE id=?", target.table, target.column),
				ciphertext,
				id,
			); err != nil {
				return err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}
