package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"reflect"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/store/model"
	"github.com/glebarez/sqlite"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func main() {
	sqlitePath := flag.String("sqlite", "", "SQLite database file path")
	pgDSN := flag.String("pg-dsn", "", "PostgreSQL DSN")
	dryRun := flag.Bool("dry-run", false, "count rows only; do not write")
	flag.Parse()
	if *sqlitePath == "" || *pgDSN == "" {
		fmt.Fprintln(os.Stderr, "usage: migrate-sqlite-pg --sqlite PATH --pg-dsn DSN [--dry-run]")
		os.Exit(2)
	}
	ctx := context.Background()
	gormConfig := &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)}
	src, err := gorm.Open(sqlite.Open("file:"+*sqlitePath+"?_pragma=foreign_keys(0)&_pragma=busy_timeout(30000)&_pragma=journal_mode(WAL)"), gormConfig)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open sqlite:", err)
		os.Exit(1)
	}
	var dst *gorm.DB
	if !*dryRun {
		dst, err = gorm.Open(postgres.Open(*pgDSN), gormConfig)
		if err != nil {
			fmt.Fprintln(os.Stderr, "open postgres:", err)
			os.Exit(1)
		}
		dstSQL, err := dst.DB()
		if err != nil {
			fmt.Fprintln(os.Stderr, "postgres pool:", err)
			os.Exit(1)
		}
		dstSQL.SetMaxOpenConns(1)
		if err := dst.WithContext(ctx).AutoMigrate(model.AllModels...); err != nil {
			fmt.Fprintln(os.Stderr, "automigrate:", err)
			os.Exit(1)
		}
		if err := dst.WithContext(ctx).Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_app_user_username ON app_user(lower(username))`).Error; err != nil {
			fmt.Fprintln(os.Stderr, "username index:", err)
			os.Exit(1)
		}
		if err := dst.WithContext(ctx).Exec(`SET session_replication_role = replica`).Error; err != nil {
			fmt.Fprintln(os.Stderr, "disable fk triggers:", err)
			os.Exit(1)
		}
	}
	total := 0
	failed := false
	for _, m := range model.AllModels {
		stmt := &gorm.Statement{DB: src}
		if err := stmt.Parse(m); err != nil {
			fmt.Fprintln(os.Stderr, "parse model:", err)
			os.Exit(1)
		}
		table := stmt.Schema.Table
		slicePtr := reflect.New(reflect.SliceOf(reflect.TypeOf(m)))
		if err := src.WithContext(ctx).Find(slicePtr.Interface()).Error; err != nil {
			if strings.Contains(err.Error(), "no such table") {
				fmt.Printf("%-28s skipped (table absent in source)\n", table)
				continue
			}
			fmt.Printf("%-28s read error: %v\n", table, err)
			failed = true
			continue
		}
		count := slicePtr.Elem().Len()
		if count == 0 {
			continue
		}
		if *dryRun {
			fmt.Printf("%-28s %d rows (dry-run)\n", table, count)
			total += count
			continue
		}
		if err := dst.WithContext(ctx).CreateInBatches(slicePtr.Interface(), 200).Error; err != nil {
			fmt.Printf("%-28s insert error: %v\n", table, err)
			failed = true
			continue
		}
		var pgCount int64
		if err := dst.WithContext(ctx).Table(table).Count(&pgCount).Error; err != nil || int(pgCount) != count {
			fmt.Printf("%-28s count mismatch: sqlite=%d pg=%d err=%v\n", table, count, pgCount, err)
			failed = true
			continue
		}
		fmt.Printf("%-28s %d rows\n", table, count)
		total += count
	}
	if !*dryRun {
		if err := dst.WithContext(ctx).Exec(`SET session_replication_role = DEFAULT`).Error; err != nil {
			fmt.Fprintln(os.Stderr, "restore fk triggers:", err)
			os.Exit(1)
		}
	}
	fmt.Printf("total %d rows\n", total)
	if failed {
		os.Exit(1)
	}
}
