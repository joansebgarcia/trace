-- queue holds accepted Events until a worker has processed them into data.db.
-- id orders the Events by commit. AUTOINCREMENT keeps ids from being reused
-- after deletes, since the workers read the queue past the last id they saw.
CREATE TABLE queue (
	id      INTEGER PRIMARY KEY AUTOINCREMENT,
	payload BLOB NOT NULL
) STRICT;
