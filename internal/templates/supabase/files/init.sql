-- GoTrue requires these enum types and schemas before its migrations run.
-- This file is mounted into the postgres container at /docker-entrypoint-initdb.d/
-- via Mortise's ConfigFile mount mechanism.

CREATE SCHEMA IF NOT EXISTS auth;
CREATE SCHEMA IF NOT EXISTS storage;
CREATE SCHEMA IF NOT EXISTS realtime;

DO $$ BEGIN CREATE TYPE auth.factor_type AS ENUM ('totp', 'webauthn'); EXCEPTION WHEN duplicate_object THEN null; END $$;
DO $$ BEGIN CREATE TYPE auth.factor_status AS ENUM ('unverified', 'verified'); EXCEPTION WHEN duplicate_object THEN null; END $$;
DO $$ BEGIN CREATE TYPE auth.aal_level AS ENUM ('aal1', 'aal2', 'aal3'); EXCEPTION WHEN duplicate_object THEN null; END $$;
DO $$ BEGIN CREATE TYPE auth.code_challenge_method AS ENUM ('s256', 'plain'); EXCEPTION WHEN duplicate_object THEN null; END $$;
DO $$ BEGIN CREATE TYPE auth.one_time_token_type AS ENUM ('confirmation_token', 'reauthentication_token', 'recovery_token', 'email_change_token_new', 'email_change_token_current', 'phone_change_token'); EXCEPTION WHEN duplicate_object THEN null; END $$;
