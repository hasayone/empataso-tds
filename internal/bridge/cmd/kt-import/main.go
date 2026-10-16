package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"

	"github.com/kuztds/bridge/ktimport"
)

// main запускает сухой прогон, живое чтение или запись в локальную kuztds.
//
// Параметры: флаги --fixture, --live, --apply.
// Результат: JSON групп и отчёт в stdout, код 0.
// Ошибки: текст в stderr, код 1. Секреты не печатаются.
func main() {
	fixture := flag.String("fixture", "testdata/export.json", "путь к снимку, если нет --live")
	live := flag.Bool("live", false, "читать Keitaro только GET по allowlist")
	apply := flag.Bool("apply", false, "записать группы в локальную админку")
	simulate := flag.String("simulate", "", "id группы для POST /api/simulate на локальной админке")
	envFile := flag.String("env", ".env", "файл с токеном и паролем локальной админки")
	flag.Parse()
	if err := run(*fixture, *live, *apply, *simulate, *envFile); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}

// run выполняет один прогон импорта.
//
// Параметры: fixture — файл снимка; live — читать API; apply — писать в kuztds; envFile — путь .env.
// Результат: nil и JSON на stdout.
// Ошибки: пустой токен, сеть, чужая форма ответа, отказ записи.
func run(fixture string, live, apply bool, simulate, envFile string) error {
	if live || apply || simulate != "" {
		if _, err := os.Stat(envFile); err == nil {
			if err := ktimport.LoadEnv(envFile); err != nil {
				return err
			}
		} else if live {
			return fmt.Errorf("нет %s", envFile)
		}
	}
	var snap ktimport.Snapshot
	if live {
		base, key, err := ktimport.RequireLiveEnv()
		if err != nil {
			return err
		}
		client := &ktimport.Client{Base: base, Key: key, HTTP: &http.Client{}}
		snap, err = client.FetchSnapshot(context.Background())
		if err != nil {
			return err
		}
	} else {
		raw, err := os.ReadFile(fixture)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &snap); err != nil {
			return fmt.Errorf("фикстура не JSON: %w", err)
		}
	}
	res := ktimport.MapAll(snap)
	groups := res.Groups
	losses := res.Losses
	if apply {
		admin, err := ktimport.AdminFromEnv()
		if err != nil {
			return err
		}
		applied, extra, err := admin.Apply(context.Background(), res)
		if err != nil {
			return err
		}
		groups = applied.Groups
		losses = append(losses, extra...)
	}
	if simulate != "" {
		admin, err := ktimport.AdminFromEnv()
		if err != nil {
			return err
		}
		selected, out, err := admin.Simulate(context.Background(), groups, simulate, "ES")
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "simulate selected=%s out=%s\n", selected, out)
	}
	res.Groups = groups
	res.Losses = losses
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(res)
}
