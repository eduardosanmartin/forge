-- Migration v4: Add model_roles support in provider config
-- Version: 4

-- No schema changes needed - model_roles is stored in JSON config
-- This migration exists to bump schema version to 4
-- Config layer handles the model_roles field in provider JSON