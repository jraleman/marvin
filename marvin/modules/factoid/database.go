package factoid

import (
	"database/sql"

	"github.com/pkg/errors"
	"github.com/riking/homeapi/marvin"
	"github.com/riking/homeapi/marvin/slack"
	"sync"
)

const (
	FactoidNameMaxLen = 75

	sqlMigrate1 = `CREATE TABLE module_factoid_factoids (
		id                SERIAL PRIMARY KEY,
		name              TEXT NOT NULL,
		channel_only      varchar(10) DEFAULT NULL, -- null OR slack.ChannelID
		rawtext           TEXT NOT NULL,

		last_set_user     varchar(10) NOT NULL, -- slack.UserID
		last_set_channel  varchar(10) NOT NULL, -- slack.ChannelID
		last_set_ts       varchar(20) NOT NULL, -- slack.MessageTS
		last_set          timestamptz NOT NULL, -- time.Time

		locked            boolean NOT NULL DEFAULT FALSE,
		forgotten         boolean NOT NULL DEFAULT FALSE
	)`

	sqlMigrate2 = `
	CREATE INDEX factoid_get ON module_factoid_factoids
	(name, channel_only, last_set, forgotten)
	WHERE forgotten = FALSE`

	// $1 = name $2 = scopeChannel
	sqlGetFactoid = `
	SELECT rawtext, last_set_user
	FROM module_factoid_factoids
	WHERE name = $1 AND (channel_only IS NULL OR channel_only = $2)
	AND forgotten = FALSE
	ORDER BY channel_only DESC, last_set DESC
	LIMIT 1`

	// $1 = name $2 = scopeChannel $3 = includeForgotten
	sqlFactoidInfo = `
	SELECT id, rawtext, channel_only, last_set_user, last_set_channel, last_set_ts, locked, forgotten
	FROM module_factoid_factoids
	WHERE name = $1 AND (channel_only IS NULL OR channel_only = $2)
	AND ($3 OR forgotten = FALSE)
	ORDER BY channel_only DESC, last_set DESC
	LIMIT 1`

	// $1 = name $2 = scopeChannel $3 = source $4 = userid $5 = msg_chan $6 = msg_ts
	sqlMakeFactoid = `
	INSERT INTO module_factoid_factoids
	(name, channel_only, rawtext, last_set_user, last_set_channel, last_set_ts, last_set)
	VALUES
	($1,   $2,           $3,      $4,            $5,               $6, CURRENT_TIMESTAMP)`

	// $1 = isLocked $2 = dbID
	sqlLockFactoid = `
	UPDATE module_factoid_factoids
	SET locked = $1
	WHERE id = $2`

	// $1 = isForgotten $2 = dbID
	sqlForgetFactoid = `
	UPDATE module_factoid_factoids
	SET forgotten = $1
	WHERE id = $2`
)

var ErrNoSuchFactoid = errors.Errorf("Factoid does not exist")

type FactoidInfo struct {
	IsBareInfo   bool
	DbID         int64
	FactoidName  string
	RawSource    string
	ScopeChannel slack.ChannelID

	LastUser    slack.UserID
	LastChannel slack.ChannelID
	LastMessage slack.MessageTS

	IsLocked    bool
	IsForgotten bool

	tokenize sync.Once
	tokens []Token
}

func (mod *FactoidModule) doMigrate(t marvin.Team) {
	t.DB().MustMigrate(Identifier, 1478236994, sqlMigrate1, sqlMigrate2)
}

func (mod *FactoidModule) doSyntaxCheck(t marvin.Team) {
	t.DB().SyntaxCheck(
		sqlGetFactoid,
		sqlFactoidInfo,
		sqlMakeFactoid,
		sqlLockFactoid,
		sqlForgetFactoid,
	)
}

func (mod *FactoidModule) GetFactoidInfo(name string, channel slack.ChannelID, withForgotten bool) (FactoidInfo, error) {
	var result FactoidInfo
	result.FactoidName = name
	result.IsBareInfo = false

	stmt, err := mod.team.DB().Prepare(sqlFactoidInfo)
	if err != nil {
		return result, errors.Wrap(err, "Database error")
	}
	defer stmt.Close()

	var scopeChannel sql.NullString

	row := stmt.QueryRow(name, string(channel), withForgotten)
	err = row.Scan(
		&result.DbID, &result.RawSource, &scopeChannel,
		(*string)(&result.LastUser), (*string)(&result.LastChannel), (*string)(&result.LastMessage),
		&result.IsLocked, &result.IsForgotten,
	)
	if err == sql.ErrNoRows {
		return result, ErrNoSuchFactoid
	} else if err != nil {
		return result, errors.Wrap(err, "Database error")
	}

	if scopeChannel.Valid {
		result.ScopeChannel = slack.ChannelID(scopeChannel.String)
	} else {
		result.ScopeChannel = ""
	}

	return result, nil
}

func (mod *FactoidModule) GetFactoidBare(name string, channel slack.ChannelID) (FactoidInfo, error) {
	var result FactoidInfo
	result.FactoidName = name
	result.IsBareInfo = true

	stmt, err := mod.team.DB().Prepare(sqlGetFactoid)
	if err != nil {
		return result, errors.Wrap(err, "Database error")
	}
	defer stmt.Close()

	row := stmt.QueryRow(name, string(channel))
	err = row.Scan(&result.RawSource, (*string)(&result.LastUser))
	if err == sql.ErrNoRows {
		return result, ErrNoSuchFactoid
	} else if err != nil {
		return result, errors.Wrap(err, "Database error")
	}
	return result, nil
}

// FillInfo transforms a bare FactoidInfo into a full FactoidInfo.
func (fi *FactoidInfo) FillInfo(mod *FactoidModule, channel slack.ChannelID) error {
	if !fi.IsBareInfo {
		return nil
	}
	newInfo, err := mod.GetFactoidInfo(fi.FactoidName, channel, false)
	if err != nil {
		return err
	}
	// I think this inspection is wrong. TODO verify
	//noinspection GoAssignmentToReceiver
	*fi = newInfo
	return nil
}

func (mod *FactoidModule) SaveFactoid(name string, channel slack.ChannelID, rawSource string, source marvin.ActionSource) error {
	if len(name) > FactoidNameMaxLen {
		return errors.Errorf("Factoid name is too long (%d > %d)", len(name), FactoidNameMaxLen)
	}
	stmt, err := mod.team.DB().Prepare(sqlMakeFactoid)
	if err != nil {
		return errors.Wrap(err, "Database error")
	}
	defer stmt.Close()

	scopeChannel := sql.NullString{Valid: channel != "", String: string(channel)}

	_, err = stmt.Exec(
		name, scopeChannel, rawSource,
		string(source.UserID()), string(source.ChannelID()), string(source.MsgTimestamp()),
	)
	if err != nil {
		return errors.Wrap(err, "Database error")
	}
	return nil
}

func (mod *FactoidModule) ForgetFactoid(dbID int64, isForgotten bool) error {
	stmt, err := mod.team.DB().Prepare(sqlForgetFactoid)
	if err != nil {
		return errors.Wrap(err, "Database error")
	}
	defer stmt.Close()

	_, err = stmt.Exec(isForgotten, dbID)
	if err != nil {
		return errors.Wrap(err, "Database error")
	}
	return nil
}

func (mod *FactoidModule) LockFactoid(dbID int64, isLocked bool) error {
	stmt, err := mod.team.DB().Prepare(sqlLockFactoid)
	if err != nil {
		return errors.Wrap(err, "Database error")
	}
	defer stmt.Close()

	_, err = stmt.Exec(isLocked, dbID)
	if err != nil {
		return errors.Wrap(err, "Database error")
	}
	return nil
}
