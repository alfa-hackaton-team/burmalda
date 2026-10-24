CREATE TABLE companies (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    synthetic_uid text NOT NULL UNIQUE CHECK (length(btrim(synthetic_uid)) > 0),
    org_type text,
    main_okved_code text,
    main_okved_name text,
    region text,
    city text,
    revenue numeric,
    workers_count integer CHECK (workers_count IS NULL OR workers_count >= 0),
    has_rko boolean,
    has_loan boolean,
    has_leasing boolean,
    has_deposit boolean,
    has_salary_project boolean,
    has_factoring boolean,
    raw_profile jsonb NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(raw_profile) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE company_groups (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    external_id text UNIQUE,
    name text,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT company_groups_values_nonblank CHECK (
        (external_id IS NULL OR length(btrim(external_id)) > 0)
        AND (name IS NULL OR length(btrim(name)) > 0)
    )
);

CREATE TABLE business_profiles (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    subject_type text NOT NULL CHECK (subject_type IN ('company', 'group')),
    company_id uuid REFERENCES companies (id) ON DELETE RESTRICT,
    group_id uuid REFERENCES company_groups (id) ON DELETE RESTRICT,
    version integer NOT NULL CHECK (version > 0),
    status text NOT NULL CHECK (status IN ('draft', 'active', 'superseded')),
    profile_data jsonb NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(profile_data) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT business_profiles_subject CHECK (
        (subject_type = 'company' AND company_id IS NOT NULL AND group_id IS NULL)
        OR (subject_type = 'group' AND company_id IS NULL AND group_id IS NOT NULL)
    ),
    CONSTRAINT business_profiles_company_id_unique UNIQUE (company_id, id),
    CONSTRAINT business_profiles_group_id_unique UNIQUE (group_id, id)
);

CREATE UNIQUE INDEX business_profiles_company_version_idx
    ON business_profiles (company_id, version) WHERE company_id IS NOT NULL;
CREATE UNIQUE INDEX business_profiles_group_version_idx
    ON business_profiles (group_id, version) WHERE group_id IS NOT NULL;

CREATE TABLE roles (
    code text PRIMARY KEY CHECK (length(btrim(code)) > 0),
    display_name text NOT NULL CHECK (length(btrim(display_name)) > 0),
    enabled boolean NOT NULL DEFAULT true
);

INSERT INTO roles (code, display_name) VALUES
    ('owner', 'Владелец'),
    ('ceo', 'Генеральный директор'),
    ('cfo', 'Финансовый директор'),
    ('chief_accountant', 'Главный бухгалтер'),
    ('treasury', 'Казначейство'),
    ('other', 'Другая роль');

CREATE TABLE business_contexts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    subject_type text NOT NULL CHECK (subject_type IN ('company', 'group')),
    company_id uuid REFERENCES companies (id) ON DELETE RESTRICT,
    group_id uuid REFERENCES company_groups (id) ON DELETE RESTRICT,
    profile_id uuid NOT NULL REFERENCES business_profiles (id) ON DELETE RESTRICT,
    role_code text NOT NULL REFERENCES roles (code) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT business_contexts_subject CHECK (
        (subject_type = 'company' AND company_id IS NOT NULL AND group_id IS NULL)
        OR (subject_type = 'group' AND company_id IS NULL AND group_id IS NOT NULL)
    ),
    CONSTRAINT business_contexts_company_profile_fk FOREIGN KEY (company_id, profile_id)
        REFERENCES business_profiles (company_id, id) ON DELETE RESTRICT,
    CONSTRAINT business_contexts_group_profile_fk FOREIGN KEY (group_id, profile_id)
        REFERENCES business_profiles (group_id, id) ON DELETE RESTRICT
);

CREATE UNIQUE INDEX business_contexts_company_profile_role_idx
    ON business_contexts (company_id, profile_id, role_code) WHERE company_id IS NOT NULL;
CREATE UNIQUE INDEX business_contexts_group_profile_role_idx
    ON business_contexts (group_id, profile_id, role_code) WHERE group_id IS NOT NULL;

CREATE TABLE users (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    identity_issuer text NOT NULL CHECK (length(btrim(identity_issuer)) > 0),
    identity_subject text NOT NULL CHECK (length(btrim(identity_subject)) > 0),
    display_name text,
    disabled_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT users_identity_unique UNIQUE (identity_issuer, identity_subject),
    CONSTRAINT users_display_name_nonblank CHECK (
        display_name IS NULL OR length(btrim(display_name)) > 0)
);

CREATE TABLE user_sessions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    session_token_hash bytea NOT NULL UNIQUE
        CHECK (octet_length(session_token_hash) = 32),
    csrf_token_hash bytea NOT NULL CHECK (octet_length(csrf_token_hash) = 32),
    authenticated_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT user_sessions_expiry CHECK (expires_at > authenticated_at),
    CONSTRAINT user_sessions_revocation CHECK (
        revoked_at IS NULL OR revoked_at >= authenticated_at
    )
);

CREATE INDEX user_sessions_active_user_idx ON user_sessions (user_id, expires_at)
    WHERE revoked_at IS NULL;

CREATE TABLE context_access_grants (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    company_id uuid REFERENCES companies (id) ON DELETE RESTRICT,
    group_id uuid REFERENCES company_groups (id) ON DELETE RESTRICT,
    role_code text NOT NULL REFERENCES roles (code) ON DELETE RESTRICT,
    granted_by uuid REFERENCES users (id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz,
    revoked_at timestamptz,
    CONSTRAINT context_access_grants_subject CHECK (
        (company_id IS NOT NULL AND group_id IS NULL)
        OR (company_id IS NULL AND group_id IS NOT NULL)
    ),
    CONSTRAINT context_access_grants_expiry CHECK (
        expires_at IS NULL OR expires_at > created_at
    ),
    CONSTRAINT context_access_grants_revocation CHECK (
        revoked_at IS NULL OR revoked_at >= created_at
    )
);

CREATE UNIQUE INDEX context_access_grants_active_company_idx
    ON context_access_grants (user_id, company_id, role_code)
    WHERE revoked_at IS NULL AND company_id IS NOT NULL;
CREATE UNIQUE INDEX context_access_grants_active_group_idx
    ON context_access_grants (user_id, group_id, role_code)
    WHERE revoked_at IS NULL AND group_id IS NOT NULL;
CREATE INDEX context_access_grants_active_user_idx
    ON context_access_grants (user_id, expires_at) WHERE revoked_at IS NULL;

CREATE TABLE admin_permission_grants (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    permission text NOT NULL CHECK (
        permission IN ('sources:read', 'sources:manage', 'sources:trust')
    ),
    granted_by uuid REFERENCES users (id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz,
    revoked_at timestamptz,
    CONSTRAINT admin_permission_grants_expiry CHECK (
        expires_at IS NULL OR expires_at > created_at
    ),
    CONSTRAINT admin_permission_grants_revocation CHECK (
        revoked_at IS NULL OR revoked_at >= created_at
    )
);

CREATE UNIQUE INDEX admin_permission_grants_active_idx
    ON admin_permission_grants (user_id, permission) WHERE revoked_at IS NULL;
CREATE INDEX admin_permission_grants_active_user_idx
    ON admin_permission_grants (user_id, expires_at) WHERE revoked_at IS NULL;
