-- Services offered by the business.
CREATE TABLE services (
    id           SERIAL PRIMARY KEY,
    name         TEXT        NOT NULL,
    duration_min INTEGER     NOT NULL CHECK (duration_min > 0),
    price_cents  INTEGER     NOT NULL CHECK (price_cents >= 0),
    is_active    BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE bookings (
    id            BIGSERIAL PRIMARY KEY,
    user_id       BIGINT      NOT NULL,
    chat_id       BIGINT      NOT NULL,
    customer_name TEXT        NOT NULL,
    service_id    INTEGER     NOT NULL REFERENCES services (id),
    starts_at     TIMESTAMPTZ NOT NULL,
    ends_at       TIMESTAMPTZ NOT NULL,
    status        TEXT        NOT NULL DEFAULT 'active'
                  CHECK (status IN ('active', 'cancelled')),
    reminder_sent BOOLEAN     NOT NULL DEFAULT FALSE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (ends_at > starts_at),

    -- The database itself guarantees that two active bookings never overlap,
    -- even when two customers press "Confirm" at the same millisecond.
    CONSTRAINT bookings_no_overlap
        EXCLUDE USING gist (tstzrange(starts_at, ends_at) WITH &&)
        WHERE (status = 'active')
);

CREATE INDEX bookings_user_active_idx ON bookings (user_id, starts_at) WHERE status = 'active';
CREATE INDEX bookings_reminder_idx    ON bookings (starts_at) WHERE status = 'active' AND NOT reminder_sent;
