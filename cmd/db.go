package cmd

import (
	"bytes"
	"context"
	"database/sql"
	"log"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	_ "github.com/mattn/go-sqlite3"
	"github.com/stephenafamo/warden/models"
	"github.com/volatiletech/sqlboiler/boil"
)

const (
	stateNotConfigured    = "not configured"
	stateToConfigureHttps = "to configure https"
	stateToDisableHttp    = "to disable http"
	stateConfigured       = "configured"
)

func createTables(db *sql.DB) error {

	tx, err := db.Begin()
	if err != nil {
		return err
	}

	_, err = tx.Exec(`CREATE TABLE IF NOT EXISTS files (
		id INTEGER NOT NULL PRIMARY KEY,
		path TEXT NOT NULL UNIQUE,
		name TEXT NOT NULL,
		content TEXT NOT NULL,
		is_configured BOOLEAN NOT NULL DEFAULT FALSE,
		last_modified DATETIME NOT NULL
	);`)
	if err != nil {
		return err
	}

	// name is the name of the service in the config file
	// reconfig is to know when to reconfigure the service.
	// Reconfig is set to true when the service should be reconfigured...
	// Such as if the parent file is modified
	// file is the parent file that contains the service config.
	// if the file is deleted it will be set to null.
	// if a domain cannot find its config in the parent file, file_id is set to null
	// a worker will clean up services whose file_id is null
	_, err = tx.Exec(`CREATE TABLE IF NOT EXISTS services (
		id INTEGER NOT NULL PRIMARY KEY,
		file_id INTEGER REFERENCES files (id) ON DELETE CASCADE ON UPDATE CASCADE,
		name TEXT NOT NULL,
		content TEXT NOT NULL,
		state TEXT NOT NULL,
		last_modified DATETIME NOT NULL
	);`)
	if err != nil {
		return err
	}

	_, err = tx.Exec(`CREATE TABLE IF NOT EXISTS nginx_config_files (
		id INTEGER NOT NULL PRIMARY KEY,
		service_id INTEGER REFERENCES services (id) ON DELETE SET NULL ON UPDATE CASCADE,
		type TEXT NOT NULL,
		path TEXT NOT NULL UNIQUE,
		last_modified DATETIME NOT NULL
	);`)
	if err != nil {
		return err
	}

	err = tx.Commit()
	if err != nil {
		return err
	}

	return nil
}

func getFileContent(path string) ([]byte, error) {
	var data []byte
	// Open file for reading
	file, err := os.Open(path)
	if err != nil {
		return data, err
	}

	data, err = ioutil.ReadAll(file)
	if err != nil {
		return data, err
	}

	return data, nil
}

func addFile(db *sql.DB, file FilePathAndInfo) error {

	content, err := getFileContent(file.Path)
	if err != nil {
		return err
	}

	var fModel = models.File{
		Name:         strings.TrimSuffix(file.Name(), filepath.Ext(file.Name())),
		Path:         file.Path,
		Content:      string(content),
		LastModified: file.ModTime(),
		IsConfigured: false,
	}

	err = fModel.Insert(context.Background(), db, boil.Infer())
	if err != nil {
		return err
	}

	log.Printf("ADDED: %s\n", file.Path)
	return nil
}

func updateFile(db *sql.DB, oldFile *models.File, file FilePathAndInfo) error {

	content, err := getFileContent(file.Path)
	if err != nil {
		return err
	}

	oldFile.Content = string(content)
	oldFile.IsConfigured = false
	oldFile.LastModified = file.ModTime()

	_, err = oldFile.Update(context.Background(), db, boil.Infer())
	if err != nil {
		return err
	}
	log.Printf("UPDATED: %s\n", file.Path)
	return nil
}

func configureServices(db *sql.DB, file *models.File) error {
	ctx := context.Background()

	var configs map[string]ServiceConfig

	if _, err := toml.Decode(file.Content, &configs); err != nil {
		return err
	}

	for key, config := range configs {
		var b bytes.Buffer
		encoder := toml.NewEncoder(&b)
		encoder.Encode(config)

		service := &models.Service{
			Name:         key,
			Content:      b.String(),
			State:        stateNotConfigured,
			LastModified: file.LastModified,
		}

		// Just add a new relationship. The cleaner cleans the old ones
		err := file.AddServices(ctx, db, true, service)
		if err != nil {
			return err
		}
	}

	log.Printf("ADDED SERVICES FOR: %s\n", file.Path)
	return nil
}
