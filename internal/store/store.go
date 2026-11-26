package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"promobot/internal/ai"
)

// Sender identifies which client delivers a channel's posts.
const (
	SenderBot       = "bot"
	SenderAssistant = "assistant"
)

// Media is an optional attachment for a broadcast or scheduled post.
type Media struct {
	Kind     string `bson:"kind" json:"kind"`     // photo|video|audio|document|animation
	Source   string `bson:"source" json:"source"` // local path or remote file id
	IsRemote bool   `bson:"is_remote" json:"is_remote"`
	Caption  string `bson:"caption" json:"caption"`
}

// Channel is a chat the bot/assistant is allowed to post to.
type Channel struct {
	ChatID  int64  `bson:"chat_id" json:"chat_id"`
	Title   string `bson:"title" json:"title"`
	Sender  string `bson:"sender" json:"sender"`
	AddedAt int64  `bson:"added_at" json:"added_at"`
}

// AssistantAccount is a connected assistant (userbot) account.
type AssistantAccount struct {
	ID      string `bson:"_id" json:"id"`    // the account's Telegram user id, as a string
	Session string `bson:"session" json:"-"` // secret Pyrogram session string (never serialized)
	UserID  int64  `bson:"user_id" json:"user_id"`
	AddedAt int64  `bson:"added_at" json:"added_at"`
}

// User is a Telegram user who has interacted with the bot (a "served" user),
// eligible to receive /broadcast -user direct messages.
type User struct {
	UserID  int64  `bson:"user_id" json:"user_id"`
	Name    string `bson:"name,omitempty" json:"name,omitempty"`
	AddedAt int64  `bson:"added_at" json:"added_at"`
	LastAt  int64  `bson:"last_at" json:"last_at"`
}

// Schedule is a recurring announcement.
type Schedule struct {
	ID              string `bson:"_id" json:"id"`
	Name            string `bson:"name" json:"name"`
	ChatID          int64  `bson:"chat_id" json:"chat_id"`
	Text            string `bson:"text" json:"text"`
	Media           *Media `bson:"media,omitempty" json:"media,omitempty"`
	IntervalMinutes int    `bson:"interval_minutes" json:"interval_minutes"`
	Enabled         bool   `bson:"enabled" json:"enabled"`
	CreatedAt       int64  `bson:"created_at" json:"created_at"`
	LastSentAt      int64  `bson:"last_sent_at" json:"last_sent_at"`
	NextSentAt      int64  `bson:"next_sent_at" json:"next_sent_at"`
}

// Store is the persistence contract used by Promobot.
type Store interface {
	Close(ctx context.Context) error

	UpsertChannel(ctx context.Context, ch Channel) error
	DeleteChannel(ctx context.Context, chatID int64) error
	GetChannel(ctx context.Context, chatID int64) (*Channel, error)
	ListChannels(ctx context.Context) ([]Channel, error)

	InsertSchedule(ctx context.Context, s Schedule) error
	UpdateSchedule(ctx context.Context, s Schedule) error
	DeleteSchedule(ctx context.Context, id string) error
	GetSchedule(ctx context.Context, id string) (*Schedule, error)
	ListSchedules(ctx context.Context) ([]Schedule, error)
	DeleteSchedulesByChatID(ctx context.Context, chatID int64) (int64, error)

	AddStickerToPack(ctx context.Context, mood, fileID string, addedBy int64) error
	RemoveStickerFromPack(ctx context.Context, mood, fileID string) (bool, error)
	GetStickersForMood(ctx context.Context, mood string) ([]string, error)
	ListStickerPacks(ctx context.Context) (map[string]int, error)
	ClearStickerPack(ctx context.Context, mood string) (int, error)

	SetChatbotEnabled(ctx context.Context, chatID int64, enabled bool) error
	IsChatbotEnabled(ctx context.Context, chatID int64) (bool, error)

	SetSetting(ctx context.Context, key, value string) error
	GetSetting(ctx context.Context, key string) (string, error)
	DelSetting(ctx context.Context, key string) error

	// Conversation memory, scoped by an account-namespaced key.
	LoadMemory(ctx context.Context, key string, max int) ([]ai.Turn, error)
	SaveMemory(ctx context.Context, key string, turns []ai.Turn) error
	ClearMemory(ctx context.Context, key string) error
	ClearMemoryByPrefix(ctx context.Context, prefix string) (int64, error)

	// Assistant (userbot) accounts.
	AddAccount(ctx context.Context, acc AssistantAccount) error
	ListAccounts(ctx context.Context) ([]AssistantAccount, error)
	DeleteAccount(ctx context.Context, id string) error
	DeleteAllAccounts(ctx context.Context) (int64, error)

	// Served users (people who have interacted with the bot), used by
	// /broadcast -user.
	UpsertUser(ctx context.Context, u User) error
	ListUsers(ctx context.Context) ([]int64, error)
	CountUsers(ctx context.Context) (int64, error)
	DeleteUser(ctx context.Context, userID int64) error
}

// MongoStore is a MongoDB-backed Store implementation.
type MongoStore struct {
	client       *mongo.Client
	db           *mongo.Database
	channels     *mongo.Collection
	schedules    *mongo.Collection
	stickerPacks *mongo.Collection
	chatSettings *mongo.Collection
	settings     *mongo.Collection
	memory       *mongo.Collection
	accounts     *mongo.Collection
	users        *mongo.Collection
}

// NewMongo connects to MongoDB and verifies the connection with a ping.
func NewMongo(ctx context.Context, uri, database string) (*MongoStore, error) {
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(uri))
	if err != nil {
		return nil, fmt.Errorf("connect mongo: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx, nil); err != nil {
		return nil, fmt.Errorf("ping mongo: %w", err)
	}
	db := client.Database(database)
	s := &MongoStore{
		client:       client,
		db:           db,
		channels:     db.Collection("channels"),
		schedules:    db.Collection("schedules"),
		stickerPacks: db.Collection("sticker_packs"),
		chatSettings: db.Collection("chat_settings"),
		settings:     db.Collection("settings"),
		memory:       db.Collection("ai_memory"),
		accounts:     db.Collection("assistant_accounts"),
		users:        db.Collection("users"),
	}
	if err := s.ensureIndexes(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *MongoStore) ensureIndexes(ctx context.Context) error {
	_, err := s.channels.Indexes().CreateOne(ctx, mongoIndex(bson.D{{Key: "chat_id", Value: 1}}, true))
	if err != nil {
		return fmt.Errorf("channel index: %w", err)
	}
	if _, err := s.users.Indexes().CreateOne(ctx, mongoIndex(bson.D{{Key: "user_id", Value: 1}}, true)); err != nil {
		return fmt.Errorf("user index: %w", err)
	}
	return nil
}

func (s *MongoStore) Close(ctx context.Context) error {
	return s.client.Disconnect(ctx)
}

func (s *MongoStore) UpsertChannel(ctx context.Context, ch Channel) error {
	filter := bson.M{"chat_id": ch.ChatID}
	update := bson.M{"$set": bson.M{
		"title":    ch.Title,
		"sender":   ch.Sender,
		"added_at": ch.AddedAt,
	}}
	_, err := s.channels.UpdateOne(ctx, filter, update, options.Update().SetUpsert(true))
	return err
}

func (s *MongoStore) DeleteChannel(ctx context.Context, chatID int64) error {
	_, err := s.channels.DeleteOne(ctx, bson.M{"chat_id": chatID})
	return err
}

func (s *MongoStore) GetChannel(ctx context.Context, chatID int64) (*Channel, error) {
	var ch Channel
	err := s.channels.FindOne(ctx, bson.M{"chat_id": chatID}).Decode(&ch)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, fmt.Errorf("channel %d not found", chatID)
	}
	if err != nil {
		return nil, err
	}
	return &ch, nil
}

func (s *MongoStore) ListChannels(ctx context.Context) ([]Channel, error) {
	cur, err := s.channels.Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "added_at", Value: 1}}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []Channel
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *MongoStore) InsertSchedule(ctx context.Context, sch Schedule) error {
	if sch.ID == "" {
		sch.ID = NewID()
	}
	_, err := s.schedules.InsertOne(ctx, sch)
	return err
}

func (s *MongoStore) UpdateSchedule(ctx context.Context, sch Schedule) error {
	_, err := s.schedules.ReplaceOne(ctx, bson.M{"_id": sch.ID}, sch)
	return err
}

func (s *MongoStore) DeleteSchedule(ctx context.Context, id string) error {
	_, err := s.schedules.DeleteOne(ctx, bson.M{"_id": id})
	return err
}

func (s *MongoStore) GetSchedule(ctx context.Context, id string) (*Schedule, error) {
	var sch Schedule
	err := s.schedules.FindOne(ctx, bson.M{"_id": id}).Decode(&sch)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, fmt.Errorf("schedule %s not found", id)
	}
	if err != nil {
		return nil, err
	}
	return &sch, nil
}

func (s *MongoStore) ListSchedules(ctx context.Context) ([]Schedule, error) {
	cur, err := s.schedules.Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "created_at", Value: 1}}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []Schedule
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *MongoStore) DeleteSchedulesByChatID(ctx context.Context, chatID int64) (int64, error) {
	res, err := s.schedules.DeleteMany(ctx, bson.M{"chat_id": chatID})
	if err != nil {
		return 0, err
	}
	return res.DeletedCount, nil
}

func (s *MongoStore) SetSetting(ctx context.Context, key, value string) error {
	filter := bson.M{"_id": key}
	update := bson.M{"$set": bson.M{"value": value, "updated_at": time.Now().Unix()}}
	_, err := s.settings.UpdateOne(ctx, filter, update, options.Update().SetUpsert(true))
	return err
}

func (s *MongoStore) GetSetting(ctx context.Context, key string) (string, error) {
	var doc struct {
		Value string `bson:"value"`
	}
	err := s.settings.FindOne(ctx, bson.M{"_id": key}).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return doc.Value, nil
}

func (s *MongoStore) DelSetting(ctx context.Context, key string) error {
	_, err := s.settings.DeleteOne(ctx, bson.M{"_id": key})
	return err
}

// memoryDoc is the stored conversation history for one (account, user) key.
type memoryDoc struct {
	ID      string    `bson:"_id"`
	Turns   []ai.Turn `bson:"turns"`
	Updated int64     `bson:"updated_at"`
}

func (s *MongoStore) LoadMemory(ctx context.Context, key string, max int) ([]ai.Turn, error) {
	if key == "" {
		return nil, nil
	}
	var doc memoryDoc
	err := s.memory.FindOne(ctx, bson.M{"_id": key}).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	turns := doc.Turns
	if max > 0 && len(turns) > max {
		turns = turns[len(turns)-max:]
	}
	return turns, nil
}

func (s *MongoStore) SaveMemory(ctx context.Context, key string, turns []ai.Turn) error {
	if key == "" {
		return nil
	}
	_, err := s.memory.UpdateOne(ctx, bson.M{"_id": key}, bson.M{"$set": bson.M{
		"turns":      turns,
		"updated_at": time.Now().Unix(),
	}}, options.Update().SetUpsert(true))
	return err
}

func (s *MongoStore) ClearMemory(ctx context.Context, key string) error {
	if key == "" {
		return nil
	}
	_, err := s.memory.DeleteOne(ctx, bson.M{"_id": key})
	return err
}

// ClearMemoryByPrefix deletes every memory document whose id begins with
// prefix+"|", returning the number removed.
func (s *MongoStore) ClearMemoryByPrefix(ctx context.Context, prefix string) (int64, error) {
	if prefix == "" {
		return 0, nil
	}
	filter := bson.M{"_id": bson.M{"$regex": "^" + regexp.QuoteMeta(prefix) + "\\|"}}
	res, err := s.memory.DeleteMany(ctx, filter)
	if err != nil {
		return 0, err
	}
	return res.DeletedCount, nil
}

func (s *MongoStore) AddAccount(ctx context.Context, acc AssistantAccount) error {
	if acc.ID == "" {
		return fmt.Errorf("account id is required")
	}
	filter := bson.M{"_id": acc.ID}
	update := bson.M{"$set": bson.M{
		"session":  acc.Session,
		"user_id":  acc.UserID,
		"added_at": acc.AddedAt,
	}}
	_, err := s.accounts.UpdateOne(ctx, filter, update, options.Update().SetUpsert(true))
	return err
}

func (s *MongoStore) ListAccounts(ctx context.Context) ([]AssistantAccount, error) {
	cur, err := s.accounts.Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "added_at", Value: 1}}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []AssistantAccount
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *MongoStore) DeleteAccount(ctx context.Context, id string) error {
	_, err := s.accounts.DeleteOne(ctx, bson.M{"_id": id})
	return err
}

func (s *MongoStore) DeleteAllAccounts(ctx context.Context) (int64, error) {
	res, err := s.accounts.DeleteMany(ctx, bson.M{})
	if err != nil {
		return 0, err
	}
	return res.DeletedCount, nil
}

func (s *MongoStore) UpsertUser(ctx context.Context, u User) error {
	if u.UserID == 0 {
		return fmt.Errorf("user id is required")
	}
	now := time.Now().Unix()
	set := bson.M{"last_at": now}
	if u.Name != "" {
		set["name"] = u.Name
	}
	_, err := s.users.UpdateOne(ctx,
		bson.M{"user_id": u.UserID},
		bson.M{
			"$set":         set,
			"$setOnInsert": bson.M{"user_id": u.UserID, "added_at": now},
		},
		options.Update().SetUpsert(true))
	return err
}

func (s *MongoStore) ListUsers(ctx context.Context) ([]int64, error) {
	cur, err := s.users.Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "added_at", Value: 1}}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []int64
	for cur.Next(ctx) {
		var doc struct {
			UserID int64 `bson:"user_id"`
		}
		if err := cur.Decode(&doc); err != nil {
			return nil, err
		}
		if doc.UserID != 0 {
			out = append(out, doc.UserID)
		}
	}
	return out, cur.Err()
}

func (s *MongoStore) CountUsers(ctx context.Context) (int64, error) {
	return s.users.CountDocuments(ctx, bson.M{})
}

func (s *MongoStore) DeleteUser(ctx context.Context, userID int64) error {
	_, err := s.users.DeleteOne(ctx, bson.M{"user_id": userID})
	return err
}

// NewID returns a short, random, hex identifier.
func NewID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func mongoIndex(keys bson.D, unique bool) mongo.IndexModel {
	return mongo.IndexModel{
		Keys:    keys,
		Options: options.Index().SetUnique(unique),
	}
}

type stickerPackDoc struct {
	ID      string   `bson:"_id"`
	FileIDs []string `bson:"file_ids"`
	AddedBy int64    `bson:"added_by,omitempty"`
}

func (s *MongoStore) AddStickerToPack(ctx context.Context, mood, fileID string, addedBy int64) error {
	mood = strings.ToLower(strings.TrimSpace(mood))
	if mood == "" || fileID == "" {
		return fmt.Errorf("mood and file id are required")
	}
	filter := bson.M{"_id": mood}
	update := bson.M{
		"$addToSet": bson.M{"file_ids": fileID},
		"$set":      bson.M{"added_by": addedBy, "updated_at": time.Now().Unix()},
	}
	_, err := s.stickerPacks.UpdateOne(ctx, filter, update, options.Update().SetUpsert(true))
	return err
}

func (s *MongoStore) RemoveStickerFromPack(ctx context.Context, mood, fileID string) (bool, error) {
	mood = strings.ToLower(strings.TrimSpace(mood))
	res, err := s.stickerPacks.UpdateOne(ctx, bson.M{"_id": mood}, bson.M{"$pull": bson.M{"file_ids": fileID}})
	if err != nil {
		return false, err
	}
	return res.ModifiedCount > 0, nil
}

func (s *MongoStore) GetStickersForMood(ctx context.Context, mood string) ([]string, error) {
	mood = strings.ToLower(strings.TrimSpace(mood))
	var doc stickerPackDoc
	err := s.stickerPacks.FindOne(ctx, bson.M{"_id": mood}).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return doc.FileIDs, nil
}

func (s *MongoStore) ListStickerPacks(ctx context.Context) (map[string]int, error) {
	cur, err := s.stickerPacks.Find(ctx, bson.M{})
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	out := make(map[string]int)
	for cur.Next(ctx) {
		var doc stickerPackDoc
		if err := cur.Decode(&doc); err != nil {
			return nil, err
		}
		out[doc.ID] = len(doc.FileIDs)
	}
	return out, cur.Err()
}

func (s *MongoStore) ClearStickerPack(ctx context.Context, mood string) (int, error) {
	mood = strings.ToLower(strings.TrimSpace(mood))
	var doc stickerPackDoc
	_ = s.stickerPacks.FindOne(ctx, bson.M{"_id": mood}).Decode(&doc)
	res, err := s.stickerPacks.DeleteOne(ctx, bson.M{"_id": mood})
	if err != nil {
		return 0, err
	}
	if res.DeletedCount == 0 {
		return 0, nil
	}
	return len(doc.FileIDs), nil
}

type chatSettingsDoc struct {
	ID      int64 `bson:"_id"`
	Enabled bool  `bson:"enabled"`
}

func (s *MongoStore) SetChatbotEnabled(ctx context.Context, chatID int64, enabled bool) error {
	filter := bson.M{"_id": chatID}
	update := bson.M{"$set": bson.M{"enabled": enabled}}
	_, err := s.chatSettings.UpdateOne(ctx, filter, update, options.Update().SetUpsert(true))
	return err
}

func (s *MongoStore) IsChatbotEnabled(ctx context.Context, chatID int64) (bool, error) {
	var doc chatSettingsDoc
	err := s.chatSettings.FindOne(ctx, bson.M{"_id": chatID}).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return doc.Enabled, nil
}
