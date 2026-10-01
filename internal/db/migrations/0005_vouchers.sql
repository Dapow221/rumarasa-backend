-- Gift vouchers the owner issues and sends to a member. Codes look like
-- RR45012026: "RR", two random digits, then the month and year of issue.
-- A voucher is single-use and can only be redeemed by the member it is
-- assigned to.
CREATE TABLE vouchers (
    id           serial PRIMARY KEY,
    code         varchar(10) NOT NULL UNIQUE
                 CHECK (code ~ '^RR[0-9]{2}(0[1-9]|1[0-2])[0-9]{4}$'),
    amount       integer NOT NULL CHECK (amount BETWEEN 1000 AND 100000000), -- rupiah
    note         text NOT NULL DEFAULT '',
    member_id    integer REFERENCES members(id) ON DELETE SET NULL,
    expires_at   date,                         -- last valid day (Jakarta), NULL = never expires
    status       varchar(20) NOT NULL DEFAULT 'active'
                 CHECK (status IN ('active', 'redeemed', 'void')),
    assigned_at  timestamptz,
    sent_at      timestamptz,
    redeemed_at  timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX vouchers_member_idx ON vouchers (member_id);
CREATE INDEX vouchers_status_created_idx ON vouchers (status, created_at DESC);
