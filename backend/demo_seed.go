package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
)

// These fixtures are synthetic demonstration data, not external bank records.
//
//go:embed testdata/demo_profiles.json
var demoFixtures []byte

//go:embed migrations/*.sql
var migrationFiles embed.FS

func prepareDemo(ctx context.Context, pool *pgxpool.Pool) error {
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		sql, e := migrationFiles.ReadFile("migrations/" + entry.Name())
		if e != nil {
			return e
		}
		if _, e = pool.Exec(ctx, string(sql)); e != nil {
			return fmt.Errorf("migration %s: %w", entry.Name(), e)
		}
	}
	return seedDemo(ctx, pool)
}

func seedDemo(ctx context.Context, pool *pgxpool.Pool) error {
	var profiles []map[string]any
	if err := json.Unmarshal(demoFixtures, &profiles); err != nil {
		return err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, s := range profiles {
		c := block(s, "company")
		var id int64
		err = tx.QueryRow(ctx, `INSERT INTO companies(inn,name,legal_form,registration_date,status,okved)
            VALUES($1,$2,$3,$4::date,$5,$6) ON CONFLICT(inn) DO UPDATE SET inn=excluded.inn RETURNING company_id`,
			c["inn"], c["name"], c["legal_form"], c["registration_date"], c["status"], c["okved"]).Scan(&id)
		if err != nil {
			return fmt.Errorf("seed company: %w", err)
		}
		for _, kind := range []string{"registry", "business_checks", "financial_profile", "bki_profile"} {
			v := s[kind]
			if kind == "registry" {
				v = c
			}
			data, err := json.Marshal(v)
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `INSERT INTO source_profiles(company_id,kind,snapshot) VALUES($1,$2,$3::jsonb) ON CONFLICT(company_id,kind) DO NOTHING`, id, kind, string(data))
			if err != nil {
				return err
			}
		}
	}
	return tx.Commit(ctx)
}
