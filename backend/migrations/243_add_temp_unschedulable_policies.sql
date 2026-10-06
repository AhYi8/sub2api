CREATE TABLE IF NOT EXISTS temp_unschedulable_policies (
    platform TEXT PRIMARY KEY,
    enabled BOOLEAN NOT NULL DEFAULT false,
    rules JSONB NOT NULL DEFAULT '[]'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'temp_unschedulable_policies_rules_array'
          AND conrelid = 'temp_unschedulable_policies'::regclass
    ) THEN
        ALTER TABLE temp_unschedulable_policies
            ADD CONSTRAINT temp_unschedulable_policies_rules_array
            CHECK (jsonb_typeof(rules) = 'array');
    END IF;
END $$;
