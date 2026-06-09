package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/database"
	"github.com/google/uuid"
)

func (cfg *apiConfig) handlerUploadVideo(w http.ResponseWriter, r *http.Request) {
	const maxUpload = 1 << 30
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload)
	err := r.ParseMultipartForm(maxUpload)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't parse form", err)
		return
	}
	videoIDString := r.PathValue("videoID")
	videoID, err := uuid.Parse(videoIDString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid ID", err)
		return
	}
	authToken, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Invalid Auth Token", err)
		return
	}

	userID, err := auth.ValidateJWT(authToken, cfg.jwtSecret)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Invalid Auth Token", err)
		return
	}

	dbVideo, err := cfg.db.GetVideo(videoID)
	if err != nil || dbVideo.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "Unauthorized", err)
		return
	}

	videoFile, fileHeaders, err := r.FormFile("video")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error processing video upload", err)
		return
	}
	defer videoFile.Close()

	mediaType, _, err := mime.ParseMediaType(fileHeaders.Header.Get("Content-Type"))
	if err != nil || mediaType != "video/mp4" {
		respondWithError(w, http.StatusBadRequest, "Invalid filetype, must be .mp4", err)
		return
	}

	tempFile, err := os.CreateTemp("", "tubely-upload.mp4")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Error creating temp file", err)
		return
	}
	defer os.Remove(tempFile.Name())
	defer tempFile.Close()
	_, err = io.Copy(tempFile, videoFile)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error copying video", err)
		return
	}
	_, err = tempFile.Seek(0, io.SeekStart)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error reseting seek", err)
		return
	}

	ratio, err := getVideoAspectRatio(tempFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error retrieving aspect ratio", err)
		return
	}
	switch ratio {
	case "9:16":
		videoIDString = "portrait/" + videoIDString + ".mp4"
	case "16:9":
		videoIDString = "landscape/" + videoIDString + ".mp4"
	default:
		videoIDString = "other/" + videoIDString + ".mp4"
	}
	videoUrl := cfg.s3Bucket + "," + videoIDString

	path, err := processVideoForFastStart(tempFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error processing video", err)
		return
	}

	processedVideo, err := os.Open(path)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error opening processed video file", err)
		return
	}
	_, err = cfg.s3Client.PutObject(r.Context(), &s3.PutObjectInput{
		Bucket:      &cfg.s3Bucket,
		Key:         &videoUrl,
		Body:        processedVideo,
		ContentType: &mediaType,
	})
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error storing video information", err)
		return
	}
	//videoURL := fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", cfg.s3Bucket, cfg.s3Region, videoIDString)
	
	dbVideo.VideoURL = &videoUrl

	err = cfg.db.UpdateVideo(dbVideo)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error updating video", err)
		return
	}
	
	signedVideo, err := cfg.dbVideoToSignedVideo(dbVideo)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error signing video", err)
		return
	}

	 respondWithJSON(w, http.StatusOK, signedVideo)
}

func getVideoAspectRatio(filePath string) (string, error) {
	cmd := exec.Command("ffprobe", "-v", "error", "-print_format", "json", "-show_streams", filePath)
	buffer := bytes.Buffer{}
	cmd.Stdout = &buffer
	err := cmd.Run()
	if err != nil {
		return "", err
	}

	type dimensions struct {
		Streams []struct {
			Width  int `json:"width"`
			Height int `json:"height"`
		} `json:"streams"`
	}
	var dims dimensions
	err = json.Unmarshal(buffer.Bytes(), &dims)
	if err != nil {
		return "", err
	}
	if len(dims.Streams) < 1 {
		return "", fmt.Errorf("Error retrieving video data")
	}

	if dims.Streams[0].Width > dims.Streams[0].Height {
		return "16:9", nil
	}
	if dims.Streams[0].Width < dims.Streams[0].Height {
		return "9:16", nil
	}
	return "other", nil
}

func processVideoForFastStart(filepath string) (string, error) {
	outputPath := filepath + ".processing"
	cmd := exec.Command("ffmpeg", "-i", filepath, "-c", "copy", "-movflags", "faststart", "-f", "mp4", outputPath)
	err := cmd.Run()
	if err != nil {
		return "", err
	}
	return outputPath, nil
}

func generatePresignedURL(s3Client *s3.Client, bucket, key string, expireTime time.Duration) (string, error) {
	presignCL := s3.NewPresignClient(s3Client)
	signedReq, err := presignCL.PresignGetObject(context.Background(), &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)}, s3.WithPresignExpires(expireTime))
	if err != nil {
		return "", err
	}
	return signedReq.URL, nil
}

func (cfg *apiConfig) dbVideoToSignedVideo(video database.Video) (database.Video, error) {
	keys := strings.Split(*video.VideoURL, ",")
	if len(keys) != 2 {
		return video, fmt.Errorf("Error parsing keystring")
	}
	newURL, err := generatePresignedURL(cfg.s3Client, keys[0], keys[1], time.Minute*5)
	if err != nil {
		return video, err
	}
	video.VideoURL = &newURL
	return video, nil
}
