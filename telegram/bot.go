package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Bot struct {
	Token      string
	apiBaseURL string
	fileBase   string
	httpClient *http.Client
	Self       User
}

type apiResponse[T any] struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
	Result      T      `json:"result"`
}

type Update struct {
	UpdateID int      `json:"update_id"`
	Message  *Message `json:"message"`
}

type User struct {
	ID       int64  `json:"id"`
	IsBot    bool   `json:"is_bot"`
	UserName string `json:"username"`
}

type Chat struct {
	ID int64 `json:"id"`
}

type Message struct {
	MessageID int         `json:"message_id"`
	Date      int64       `json:"date"`
	Chat      Chat        `json:"chat"`
	From      *User       `json:"from"`
	Text      string      `json:"text"`
	Photo     []PhotoSize `json:"photo"`
}

type PhotoSize struct {
	FileID string `json:"file_id"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

type File struct {
	FileID   string `json:"file_id"`
	FilePath string `json:"file_path"`
}

type InputMediaPhoto struct {
	Type    string `json:"type"`
	Media   string `json:"media"`
	Caption string `json:"caption,omitempty"`
}

type MediaGroupMessage struct {
	MessageID int `json:"message_id"`
}

func NewBot(token string) (*Bot, error) {
	b := &Bot{
		Token:      token,
		apiBaseURL: "https://api.telegram.org/bot" + token,
		fileBase:   "https://api.telegram.org/file/bot" + token,
		httpClient: &http.Client{Timeout: 70 * time.Second},
	}
	self, err := b.GetMe(context.Background())
	if err != nil {
		return nil, err
	}
	b.Self = *self
	return b, nil
}

func (m *Message) IsCommand() bool {
	return strings.HasPrefix(strings.TrimSpace(m.Text), "/")
}

func (m *Message) Command() string {
	text := strings.TrimSpace(m.Text)
	if !strings.HasPrefix(text, "/") {
		return ""
	}
	text = strings.TrimPrefix(text, "/")
	space := strings.IndexByte(text, ' ')
	if space >= 0 {
		text = text[:space]
	}
	if at := strings.IndexByte(text, '@'); at >= 0 {
		text = text[:at]
	}
	return text
}

func (m *Message) CommandArguments() string {
	text := strings.TrimSpace(m.Text)
	if !strings.HasPrefix(text, "/") {
		return ""
	}
	space := strings.IndexByte(text, ' ')
	if space < 0 {
		return ""
	}
	return strings.TrimSpace(text[space+1:])
}

func (b *Bot) GetMe(ctx context.Context) (*User, error) {
	var resp apiResponse[User]
	if err := b.doJSON(ctx, http.MethodGet, "/getMe", nil, &resp); err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, fmt.Errorf("telegram getMe failed: %s", resp.Description)
	}
	return &resp.Result, nil
}

func (b *Bot) GetUpdates(ctx context.Context, offset int, timeoutSeconds int) ([]Update, error) {
	payload := map[string]any{"offset": offset, "timeout": timeoutSeconds, "allowed_updates": []string{"message"}}
	var resp apiResponse[[]Update]
	if err := b.doJSON(ctx, http.MethodPost, "/getUpdates", payload, &resp); err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, fmt.Errorf("telegram getUpdates failed: %s", resp.Description)
	}
	return resp.Result, nil
}

func (b *Bot) SendMessage(ctx context.Context, chatID int64, text string, disableWebPreview bool) error {
	payload := map[string]any{"chat_id": chatID, "text": text, "disable_web_page_preview": disableWebPreview}
	var resp apiResponse[Message]
	if err := b.doJSON(ctx, http.MethodPost, "/sendMessage", payload, &resp); err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("telegram sendMessage failed: %s", resp.Description)
	}
	return nil
}

func (b *Bot) SendPhoto(ctx context.Context, chatID int64, filePath, caption string) error {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	_ = writer.WriteField("chat_id", fmt.Sprintf("%d", chatID))
	if caption != "" {
		_ = writer.WriteField("caption", caption)
	}
	part, err := writer.CreateFormFile("photo", filepath.Base(filePath))
	if err != nil {
		return err
	}
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := ioCopy(part, file); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.apiBaseURL+"/sendPhoto", body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := b.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var decoded apiResponse[Message]
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return err
	}
	if !decoded.OK {
		return fmt.Errorf("telegram sendPhoto failed: %s", decoded.Description)
	}
	return nil
}

func (b *Bot) SendMediaGroup(ctx context.Context, chatID int64, filePaths []string, caption string) error {
	if len(filePaths) == 0 {
		return nil
	}

	for start := 0; start < len(filePaths); start += 10 {
		end := start + 10
		if end > len(filePaths) {
			end = len(filePaths)
		}
		if err := b.sendMediaGroupChunk(ctx, chatID, filePaths[start:end], caption, start == 0); err != nil {
			return err
		}
	}
	return nil
}

func (b *Bot) sendMediaGroupChunk(ctx context.Context, chatID int64, filePaths []string, caption string, includeCaption bool) error {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	if err := writer.WriteField("chat_id", fmt.Sprintf("%d", chatID)); err != nil {
		return err
	}

	media := make([]InputMediaPhoto, 0, len(filePaths))
	for i, filePath := range filePaths {
		fieldName := fmt.Sprintf("attach%d", i)
		item := InputMediaPhoto{Type: "photo", Media: "attach://" + fieldName}
		if i == 0 && includeCaption && caption != "" {
			item.Caption = caption
		}
		media = append(media, item)

		part, err := writer.CreateFormFile(fieldName, filepath.Base(filePath))
		if err != nil {
			return err
		}
		file, err := os.Open(filePath)
		if err != nil {
			return err
		}
		if _, err := ioCopy(part, file); err != nil {
			file.Close()
			return err
		}
		file.Close()
	}

	mediaJSON, err := json.Marshal(media)
	if err != nil {
		return err
	}
	if err := writer.WriteField("media", string(mediaJSON)); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.apiBaseURL+"/sendMediaGroup", body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := b.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var decoded apiResponse[[]MediaGroupMessage]
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return err
	}
	if !decoded.OK {
		return fmt.Errorf("telegram sendMediaGroup failed: %s", decoded.Description)
	}
	return nil
}

func (b *Bot) GetFile(ctx context.Context, fileID string) (*File, error) {
	payload := map[string]any{"file_id": fileID}
	var resp apiResponse[File]
	if err := b.doJSON(ctx, http.MethodPost, "/getFile", payload, &resp); err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, fmt.Errorf("telegram getFile failed: %s", resp.Description)
	}
	return &resp.Result, nil
}

func (b *Bot) GetFileDirectURL(ctx context.Context, fileID string) (string, error) {
	file, err := b.GetFile(ctx, fileID)
	if err != nil {
		return "", err
	}
	return b.fileBase + "/" + file.FilePath, nil
}

func (b *Bot) StopReceivingUpdates() {}

func (b *Bot) doJSON(ctx context.Context, method, path string, payload any, out any) error {
	var body bytes.Buffer
	if payload != nil {
		if err := json.NewEncoder(&body).Encode(payload); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, b.apiBaseURL+path, &body)
	if err != nil {
		return err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := b.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(out)
}

func ioCopy(dst io.Writer, src *os.File) (int64, error) {
	return io.Copy(dst, src)
}
