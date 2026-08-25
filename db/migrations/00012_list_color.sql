-- +goose Up
-- A colour per list, so a board of eight cards is scannable by more than
-- reading eight titles.
--
-- A NAME, not a hex. helva has four alternate palettes and a light appearance,
-- and a stored `#3d9aff` is correct in exactly one of those eight combinations —
-- it would be the one value in the product that ignores the theme. A name is
-- resolved to a token at paint time, so the same list reads as "red" in every
-- appearance and the palette can be retuned later without a data migration.
--
-- The CHECK is the reason the set is worth writing down here rather than only in
-- the service: `color` reaches the database from the HTTP API and from MCP, and
-- agents write to both. A free-text column would eventually hold whatever an
-- agent decided "reddish" meant, and the UI would have to render it.
--
-- NULL means no colour, which is the default and stays the default: a board
-- where every list is tinted has no emphasis left to give.
ALTER TABLE lists
    ADD COLUMN color text
    CHECK (color IS NULL OR color IN ('red', 'amber', 'green', 'teal', 'blue', 'violet', 'pink', 'slate'));

-- +goose Down
ALTER TABLE lists DROP COLUMN color;
