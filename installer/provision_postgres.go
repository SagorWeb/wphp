package main

import (
	"fmt"
	"os"
)

func installPostgreSQL(creds Credentials) {
	aptWait()
	// Install PostgreSQL from official PGDG repo (added in step 2)
	run("apt-get", "install", "-y", "-qq", "postgresql")
	run("systemctl", "enable", "postgresql")
	run("systemctl", "start", "postgresql")

	ramMB := shellOutputInt("awk '/^MemTotal:/{print int($2/1024)}' /proc/meminfo")
	sharedBuf, effCache, maintWork, workMem := calcPostgreSQLTuning(ramMB)
	cores := shellOutputInt("nproc")
	parallelGather := cores / 2
	parallelWorkers := cores
	parallelMaint := cores / 2

	if parallelGather < 2 {
		parallelGather = 2
	}
	if parallelGather > 4 {
		parallelGather = 4
	}
	if parallelWorkers < 4 {
		parallelWorkers = 4
	}
	if parallelWorkers > 8 {
		parallelWorkers = 8
	}
	if parallelMaint < 2 {
		parallelMaint = 2
	}
	if parallelMaint > 4 {
		parallelMaint = 4
	}

	// 1. Create or update wphpanel role (DO $$ block ensures clean execution)
	userSQL := fmt.Sprintf(`DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'wphpanel') THEN
        CREATE USER wphpanel WITH PASSWORD '%s' NOSUPERUSER CREATEROLE CREATEDB;
    ELSE
        ALTER USER wphpanel WITH PASSWORD '%s' NOSUPERUSER CREATEROLE CREATEDB;
    END IF;
END $$;`, creds.PostgresPass, creds.PostgresPass)
	run("sudo", "-u", "postgres", "psql", "-c", userSQL)

	// 2. Create database (standalone statement)
	run("bash", "-c", "sudo -u postgres psql -lqt | cut -d \\| -f 1 | grep -qw wphpanel || sudo -u postgres psql -c 'CREATE DATABASE wphpanel OWNER wphpanel;'")
	run("sudo", "-u", "postgres", "psql", "-c", "GRANT ALL PRIVILEGES ON DATABASE wphpanel TO wphpanel;")

	tuningSQL := fmt.Sprintf(`
ALTER SYSTEM SET shared_buffers = '%dMB';
ALTER SYSTEM SET work_mem = '%dMB';
ALTER SYSTEM SET effective_cache_size = '%dMB';
ALTER SYSTEM SET maintenance_work_mem = '%dMB';
ALTER SYSTEM SET wal_buffers = '16MB';
ALTER SYSTEM SET wal_compression = 'lz4';
ALTER SYSTEM SET max_wal_size = '2GB';
ALTER SYSTEM SET min_wal_size = '256MB';
ALTER SYSTEM SET checkpoint_completion_target = 0.9;
ALTER SYSTEM SET random_page_cost = 1.1;
ALTER SYSTEM SET effective_io_concurrency = 200;
ALTER SYSTEM SET max_parallel_workers_per_gather = %d;
ALTER SYSTEM SET max_parallel_workers = %d;
ALTER SYSTEM SET max_parallel_maintenance_workers = %d;
ALTER SYSTEM SET max_connections = 100;
ALTER SYSTEM SET idle_in_transaction_session_timeout = '300s';
ALTER SYSTEM SET statement_timeout = '60s';
ALTER SYSTEM SET log_connections = 'on';
ALTER SYSTEM SET log_disconnections = 'on';
ALTER SYSTEM SET log_statement = 'ddl';
ALTER SYSTEM SET log_min_duration_statement = 1000;
ALTER SYSTEM SET log_line_prefix = '%%m [%%p] %%u@%%d ';
ALTER SYSTEM SET shared_preload_libraries = 'pg_stat_statements';
`, sharedBuf, workMem, effCache, maintWork, parallelGather, parallelWorkers, parallelMaint)

	// Write tuning SQL and apply
	tmpSQL := "/tmp/wphpanel-pg-tune.sql"
	os.WriteFile(tmpSQL, []byte(tuningSQL), 0644)
	run("sudo", "-u", "postgres", "psql", "-f", tmpSQL)
	os.Remove(tmpSQL)

	// ── PostgreSQL auto-restart override (Issue 2) ──
	// Default postgresql@.service has Restart=no — panel DB loss = total outage.
	os.MkdirAll("/etc/systemd/system/postgresql@.service.d", 0755)
	os.WriteFile("/etc/systemd/system/postgresql@.service.d/restart.conf",
		[]byte("[Unit]\nStartLimitBurst=10\nStartLimitIntervalSec=120\n\n[Service]\nRestart=on-failure\nRestartSec=5\n"), 0644)
	run("systemctl", "daemon-reload")

	// Restart to apply shared_preload_libraries
	run("systemctl", "restart", "postgresql")

	// Enable extensions in the panel database
	run("sudo", "-u", "postgres", "psql", "-d", "wphpanel", "-c", "CREATE EXTENSION IF NOT EXISTS pg_stat_statements;")

	// ── Database-level ACL lockdown (critical for multi-tenant isolation) ────
	// Without these, any database user could CONNECT to the panel DB, template1,
	// and postgres. This is defense-in-depth alongside pgAdmin server isolation.
	run("sudo", "-u", "postgres", "psql", "-c", "REVOKE CONNECT ON DATABASE wphpanel FROM PUBLIC;")
	run("sudo", "-u", "postgres", "psql", "-c", "REVOKE CONNECT ON DATABASE postgres FROM PUBLIC;")
	run("sudo", "-u", "postgres", "psql", "-c", "REVOKE CONNECT ON DATABASE template1 FROM PUBLIC;")
	run("sudo", "-u", "postgres", "psql", "-c", "GRANT CONNECT ON DATABASE wphpanel TO wphpanel;")
}

func calcPostgreSQLTuning(ramMB int) (sharedBuf, effCache, maintWork, workMem int) {
	sharedBuf = ramMB / 4
	effCache = ramMB * 3 / 4
	maintWork = ramMB / 32
	workMem = ramMB / 512

	if sharedBuf < 256 {
		sharedBuf = 256
	}
	if sharedBuf > 8192 {
		sharedBuf = 8192
	}
	if effCache < 512 {
		effCache = 512
	}
	if maintWork < 128 {
		maintWork = 128
	}
	if maintWork > 2048 {
		maintWork = 2048
	}
	if workMem < 4 {
		workMem = 4
	}
	if workMem > 64 {
		workMem = 64
	}
	return
}
