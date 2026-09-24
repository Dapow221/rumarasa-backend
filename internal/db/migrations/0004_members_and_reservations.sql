-- Membership signups submitted from the public site. A signup starts as
-- 'pending' and only gets a member number once an admin approves it.
CREATE SEQUENCE member_no_seq START 1;

CREATE TABLE members (
    id           serial PRIMARY KEY,
    member_no    varchar(20) UNIQUE,
    name         varchar(200) NOT NULL,
    phone        varchar(20) NOT NULL UNIQUE, -- normalized: digits only, 62-prefixed
    email        varchar(254) NOT NULL,
    birthday     date NOT NULL,
    address      text NOT NULL,
    status       varchar(20) NOT NULL DEFAULT 'pending'
                 CHECK (status IN ('pending', 'active', 'rejected', 'suspended')),
    tier         varchar(20) NOT NULL DEFAULT 'silver'
                 CHECK (tier IN ('silver', 'gold', 'platinum')),
    consent_at   timestamptz NOT NULL,        -- UU PDP: when the applicant agreed to the privacy policy
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX members_status_created_idx ON members (status, created_at DESC);
CREATE INDEX members_created_idx ON members (created_at DESC);

-- Table reservations submitted from the public site, confirmed by staff.
CREATE TABLE reservations (
    id           serial PRIMARY KEY,
    name         varchar(200) NOT NULL,
    phone        varchar(20) NOT NULL,
    date         date NOT NULL,
    time         time NOT NULL,
    guests       integer NOT NULL CHECK (guests BETWEEN 1 AND 500),
    request      text NOT NULL DEFAULT '',
    status       varchar(20) NOT NULL DEFAULT 'pending'
                 CHECK (status IN ('pending', 'confirmed', 'cancelled', 'completed', 'no_show')),
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX reservations_date_time_idx ON reservations (date, time);
CREATE INDEX reservations_status_date_idx ON reservations (status, date);
