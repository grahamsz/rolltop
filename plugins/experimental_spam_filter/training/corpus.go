// Package training implements the explicit, offline spam-model training path.
// It is never imported by the production plugin.
package training

import (
	"archive/tar"
	"compress/bzip2"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"rolltop/backend/mailparse"
	spammodel "rolltop/plugins/experimental_spam_filter/model"
)

const (
	CorpusManifestVersion = "spamassassin-public-corpus-v1"
	maxArchiveBytes       = int64(128 << 20)
	maxExtractedBytes     = int64(512 << 20)
	maxCorpusMessageBytes = int64(4 << 20)
)

type CorpusSpec struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Label  string `json:"label"`
}

// CorpusSpecs is the complete, pinned V1 recipe. Changing any entry requires a
// new model version and a review of the corpus license/provenance.
var CorpusSpecs = []CorpusSpec{
	{
		Name:   "20030228_easy_ham.tar.bz2",
		URL:    "https://spamassassin.apache.org/old/publiccorpus/20030228_easy_ham.tar.bz2",
		SHA256: "2b7b65904bcfcc31d2b5f51946f2d261370b257402cbbd62930b46ab83367438",
		Label:  "ham",
	},
	{
		Name:   "20030228_easy_ham_2.tar.bz2",
		URL:    "https://spamassassin.apache.org/old/publiccorpus/20030228_easy_ham_2.tar.bz2",
		SHA256: "b4bd3dc5ae5b40f38e99a0e41ad7d16b428b56e818e3a6ffa07330d62004dd38",
		Label:  "ham",
	},
	{
		Name:   "20030228_hard_ham.tar.bz2",
		URL:    "https://spamassassin.apache.org/old/publiccorpus/20030228_hard_ham.tar.bz2",
		SHA256: "ce2ce67880643dbde65ea7f85bffbfe4417349c4bd80b6b0de56262ae6b0a9c9",
		Label:  "ham",
	},
	{
		Name:   "20030228_spam.tar.bz2",
		URL:    "https://spamassassin.apache.org/old/publiccorpus/20030228_spam.tar.bz2",
		SHA256: "c08debc32413804949a866be45ef78195cec2cbafd1da744ed76cdb860589743",
		Label:  "spam",
	},
	{
		Name:   "20050311_spam_2.tar.bz2",
		URL:    "https://spamassassin.apache.org/old/publiccorpus/20050311_spam_2.tar.bz2",
		SHA256: "44280a0e28bf7645b2279e8e42659271ad82c6f40bf8d364dbda7f1df344a765",
		Label:  "spam",
	},
}

type downloadManifest struct {
	Version string       `json:"version"`
	Corpora []CorpusSpec `json:"corpora"`
}

func Download(ctx context.Context, cacheDir string, output io.Writer) error {
	archiveDir := filepath.Join(cacheDir, "archives")
	corpusDir := filepath.Join(cacheDir, "corpus")
	if err := os.MkdirAll(archiveDir, 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(corpusDir, 0o700); err != nil {
		return err
	}
	client := &http.Client{Timeout: 3 * time.Minute}
	for _, spec := range CorpusSpecs {
		fmt.Fprintf(output, "corpus: %s\n", spec.Name)
		archivePath := filepath.Join(archiveDir, spec.Name)
		if err := downloadOne(ctx, client, spec, archivePath); err != nil {
			return err
		}
		target := filepath.Join(corpusDir, strings.TrimSuffix(spec.Name, ".tar.bz2"))
		if err := extractArchive(archivePath, target); err != nil {
			return fmt.Errorf("extract %s: %w", spec.Name, err)
		}
	}
	manifestData, err := json.MarshalIndent(downloadManifest{Version: CorpusManifestVersion, Corpora: CorpusSpecs}, "", "  ")
	if err != nil {
		return err
	}
	manifestData = append(manifestData, '\n')
	return atomicWrite(filepath.Join(cacheDir, "download-manifest.json"), manifestData, 0o600)
}

func downloadOne(ctx context.Context, client *http.Client, spec CorpusSpec, destination string) error {
	if digest, err := fileSHA256(destination); err == nil && digest == spec.SHA256 {
		return nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, spec.URL, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("download %s: %w", spec.URL, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %s", spec.URL, response.Status)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".corpus-download-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	hasher := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(temporary, hasher), io.LimitReader(response.Body, maxArchiveBytes+1))
	closeErr := temporary.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if written > maxArchiveBytes {
		return fmt.Errorf("download %s exceeded %d bytes", spec.URL, maxArchiveBytes)
	}
	digest := hex.EncodeToString(hasher.Sum(nil))
	if digest != spec.SHA256 {
		return fmt.Errorf("download %s checksum mismatch: got %s", spec.URL, digest)
	}
	if err := os.Chmod(temporaryName, 0o600); err != nil {
		return err
	}
	return os.Rename(temporaryName, destination)
}

func extractArchive(archivePath, destination string) error {
	digestMarker := filepath.Join(destination, ".complete")
	digest, err := fileSHA256(archivePath)
	if err != nil {
		return err
	}
	if marker, err := os.ReadFile(digestMarker); err == nil && strings.TrimSpace(string(marker)) == digest {
		return nil
	}
	temporary, err := os.MkdirTemp(filepath.Dir(destination), ".corpus-extract-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporary)
	archive, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer archive.Close()
	if err := extractTar(tar.NewReader(bzip2.NewReader(archive)), temporary); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(temporary, ".complete"), []byte(digest+"\n"), 0o600); err != nil {
		return err
	}
	if err := os.RemoveAll(destination); err != nil {
		return err
	}
	return os.Rename(temporary, destination)
}

func extractTar(reader *tar.Reader, destination string) error {
	var total int64
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		clean := filepath.Clean(filepath.FromSlash(header.Name))
		if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return fmt.Errorf("unsafe archive path %q", header.Name)
		}
		target := filepath.Join(destination, clean)
		relative, err := filepath.Rel(destination, target)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return fmt.Errorf("unsafe archive path %q", header.Name)
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if header.Size < 0 || header.Size > maxCorpusMessageBytes || total+header.Size > maxExtractedBytes {
				return fmt.Errorf("archive entry %q is too large", header.Name)
			}
			total += header.Size
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				return err
			}
			_, copyErr := io.CopyN(file, reader, header.Size)
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		default:
			return fmt.Errorf("archive entry %q has unsupported type %d", header.Name, header.Typeflag)
		}
	}
}

type sample struct {
	ID      string
	Source  string
	Spam    bool
	Message spammodel.Message
}

func loadCorpus(cacheDir string) ([]sample, int, error) {
	manifestData, err := os.ReadFile(filepath.Join(cacheDir, "download-manifest.json"))
	if err != nil {
		return nil, 0, fmt.Errorf("corpus is not downloaded: %w", err)
	}
	var manifest downloadManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return nil, 0, err
	}
	if manifest.Version != CorpusManifestVersion || !sameCorpusSpecs(manifest.Corpora, CorpusSpecs) {
		return nil, 0, errors.New("downloaded corpus manifest does not match the pinned recipe")
	}
	var samples []sample
	parseSkipped := 0
	for _, spec := range CorpusSpecs {
		root := filepath.Join(cacheDir, "corpus", strings.TrimSuffix(spec.Name, ".tar.bz2"))
		var paths []string
		if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.Type().IsRegular() && entry.Name() != ".complete" && !strings.HasPrefix(entry.Name(), "cmds") {
				paths = append(paths, path)
			}
			return nil
		}); err != nil {
			return nil, 0, err
		}
		sort.Strings(paths)
		sourceParseSkipped := 0
		for _, path := range paths {
			raw, err := os.ReadFile(path)
			if err != nil {
				return nil, 0, err
			}
			message, err := parseCorpusMessage(raw)
			if err != nil {
				// Production sync does not classify messages rejected by mailparse.
				// Skip them without logging parser errors, which can contain raw MIME
				// fragments, and fail below if the rejection rate is unexpectedly high.
				parseSkipped++
				sourceParseSkipped++
				continue
			}
			relative, _ := filepath.Rel(root, path)
			samples = append(samples, sample{
				ID:      filepath.ToSlash(relative),
				Source:  spec.Name,
				Spam:    spec.Label == "spam",
				Message: message,
			})
		}
		allowedFailures := len(paths)/100 + 1
		if sourceParseSkipped > allowedFailures {
			return nil, 0, fmt.Errorf("production parser rejected %d of %d messages in %s", sourceParseSkipped, len(paths), spec.Name)
		}
	}
	return deduplicate(samples), parseSkipped, nil
}

func parseCorpusMessage(raw []byte) (spammodel.Message, error) {
	if int64(len(raw)) > maxCorpusMessageBytes {
		return spammodel.Message{}, fmt.Errorf("message exceeds %d bytes", maxCorpusMessageBytes)
	}
	parsed, err := mailparse.Parse(raw)
	if err != nil {
		return spammodel.Message{}, err
	}
	attachmentTypes := make([]string, 0, len(parsed.Files))
	for _, attachment := range parsed.Files {
		if contentType := strings.TrimSpace(attachment.ContentType); contentType != "" {
			attachmentTypes = append(attachmentTypes, contentType)
		}
	}
	hasHTML := strings.TrimSpace(parsed.HTML) != ""
	mimeType := "text/plain"
	if hasHTML {
		mimeType = "text/html"
	}
	return spammodel.Message{
		Subject:         parsed.Subject,
		Body:            parsed.Text,
		From:            parsed.From,
		To:              recipientAddresses(parsed.To, parsed.CC),
		MIMEType:        mimeType,
		AttachmentTypes: attachmentTypes,
		HTML:            hasHTML,
	}, nil
}

func recipientAddresses(values ...string) []string {
	var result []string
	seen := make(map[string]bool)
	for _, value := range values {
		parsed, err := mail.ParseAddressList(value)
		if err == nil {
			for _, address := range parsed {
				clean := strings.ToLower(strings.TrimSpace(address.Address))
				if clean != "" && !seen[clean] {
					seen[clean] = true
					result = append(result, clean)
				}
			}
			continue
		}
		for _, address := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ';' }) {
			clean := strings.ToLower(strings.TrimSpace(address))
			if clean != "" && !seen[clean] {
				seen[clean] = true
				result = append(result, clean)
			}
		}
	}
	return result
}

func deduplicate(input []sample) []sample {
	sort.Slice(input, func(i, j int) bool {
		if input[i].Source == input[j].Source {
			return input[i].ID < input[j].ID
		}
		return input[i].Source < input[j].Source
	})
	seenExact := make(map[[32]byte]struct{}, len(input))
	nearBuckets := make(map[uint16][]uint64)
	result := make([]sample, 0, len(input))
	for _, item := range input {
		normalized := strings.Join(strings.Fields(strings.ToLower(item.Message.Subject+"\n"+item.Message.Body)), " ")
		exact := sha256.Sum256([]byte(normalized))
		if _, exists := seenExact[exact]; exists {
			continue
		}
		simhash := textSimHash(normalized)
		bucket := uint16(simhash >> 48)
		duplicate := false
		for _, existing := range nearBuckets[bucket] {
			if hammingDistance(simhash, existing) <= 3 {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		seenExact[exact] = struct{}{}
		nearBuckets[bucket] = append(nearBuckets[bucket], simhash)
		result = append(result, item)
	}
	return result
}

func textSimHash(value string) uint64 {
	var counts [64]int
	words := strings.Fields(value)
	if len(words) > 2048 {
		words = words[:2048]
	}
	for _, word := range words {
		hash := stableHash(word)
		for bit := 0; bit < 64; bit++ {
			if hash&(uint64(1)<<bit) != 0 {
				counts[bit]++
			} else {
				counts[bit]--
			}
		}
	}
	var result uint64
	for bit, count := range counts {
		if count >= 0 {
			result |= uint64(1) << bit
		}
	}
	return result
}

func stableHash(value string) uint64 {
	const offset = uint64(14695981039346656037)
	const prime = uint64(1099511628211)
	hash := offset
	for index := 0; index < len(value); index++ {
		hash ^= uint64(value[index])
		hash *= prime
	}
	return hash
}

func hammingDistance(left, right uint64) int {
	value := left ^ right
	count := 0
	for value != 0 {
		value &= value - 1
		count++
	}
	return count
}

func sameCorpusSpecs(left, right []CorpusSpec) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".artifact-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Chmod(temporaryName, mode); err != nil {
		return err
	}
	return os.Rename(temporaryName, path)
}
