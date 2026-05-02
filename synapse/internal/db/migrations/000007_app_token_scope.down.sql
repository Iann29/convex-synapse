-- Down: revert to the original v0.1 scope constraint. Any 'app' rows
-- present at downgrade time must be deleted first; CHECK constraint
-- creation refuses otherwise.

DELETE FROM access_tokens WHERE scope = 'app';

ALTER TABLE access_tokens
    DROP CONSTRAINT access_tokens_scope_check;

ALTER TABLE access_tokens
    ADD CONSTRAINT access_tokens_scope_check
    CHECK (scope IN ('user','team','project','deployment'));
