package mapper

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"opensync/internal/config"
	"opensync/internal/model"
	"opensync/internal/msg"
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

// ErrCredentialUnreadable reports a stored credential that exists but cannot be
// decrypted with the current secret.key — typically because the key file was
// lost or replaced. It is matched with errors.Is.
var ErrCredentialUnreadable = errors.New("stored credential cannot be decrypted")

// decryptCredentialColumn decrypts column in every row for a single-row read.
// An undecryptable value fails the read with ErrCredentialUnreadable: the
// caller is about to use (or merge edits into) this specific credential, and a
// blank stand-in would be sent to the provider or written back over the
// ciphertext.
func decryptCredentialColumn(rows []map[string]interface{}, column string) error {
	for _, row := range rows {
		value, ok := row[column]
		if !ok || value == nil {
			continue
		}
		plain, err := decryptCredential(value)
		if err != nil {
			log.Printf("Warning: %s of row %v cannot be decrypted with the current secret.key: %v", column, row["id"], err)
			// The PublicError half gives the user an actionable message through
			// the service layer's existing PublicError pass-through; the
			// sentinel half lets callers detect the condition with errors.Is.
			return fmt.Errorf("%w: %w", model.PublicError(msg.T(msg.CredentialUnreadable)), ErrCredentialUnreadable)
		}
		row[column] = plain
	}
	return nil
}

// decryptCredentialColumnLenient decrypts column for list reads. A row whose
// value cannot be decrypted has the credential blanked and is still returned,
// with a warning logged: one unreadable row must not make the whole engine or
// notification list disappear from the UI (the user needs the list to find and
// delete or re-create that row). The blank only ever reaches list consumers —
// the edit flows re-read the row through the strict single-row path, so it can
// never be merged back over the stored ciphertext.
func decryptCredentialColumnLenient(rows []map[string]interface{}, table, column string) {
	for _, row := range rows {
		value, ok := row[column]
		if !ok || value == nil {
			continue
		}
		plain, err := decryptCredential(value)
		if err != nil {
			log.Printf("Warning: %s.%s of row %v cannot be decrypted with the current secret.key and is shown as empty; re-create this entry to restore it: %v", table, column, row["id"], err)
			row[column] = ""
			continue
		}
		row[column] = plain
	}
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
				// Skipped, not fatal: InitSQL treats a migration error as fatal,
				// so one undecryptable row (e.g. after secret.key was lost) used
				// to stop the whole service from starting. The row is left
				// untouched — the ciphertext may still be recoverable with the
				// original key — and reads report it as unreadable.
				log.Printf("Warning: skipping credential migration for %s.%s row %d: cannot decrypt with the current secret.key: %v", target.table, target.column, id, err)
				continue
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
