package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

func (cfg *apiConfig) handlerUploadThumbnail(w http.ResponseWriter, r *http.Request) {
	videoIDString := r.PathValue("videoID")
	videoID, err := uuid.Parse(videoIDString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid ID", err)
		return
	}

	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't find JWT", err)
		return
	}

	userID, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't validate JWT", err)
		return
	}

	fmt.Println("uploading thumbnail for video", videoID, "by user", userID)

	// TODO: implement the upload here
	const maxMemory int64 = 10 << 20
	err = r.ParseMultipartForm(maxMemory)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't parse form", err)
		return
	}

	file_data, file_headers, err := r.FormFile("thumbnail")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't parse form", err)
		return
	}

	video, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error finding video", err)
		return
	}
	if video.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "Not the video owner", err)
		return

	}

	media_type, _, err := mime.ParseMediaType(file_headers.Header.Get("Content-Type"))
	if err != nil || media_type != "image/jpeg" && media_type != "image/png" {
		respondWithError(w, http.StatusInternalServerError, "Invalid file", err)
		return
	}
	randbyte := make([]byte, 32)
	rand.Read(randbyte)
	thumbnailId := base64.RawURLEncoding.EncodeToString((randbyte))

	file_type := strings.Split(media_type, "/")
	path := filepath.Join(cfg.assetsRoot, thumbnailId+"."+file_type[1])
	file, err := os.Create(path)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error creating thumbnail", err)
		return
	}
	_, err = io.Copy(file, file_data)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error saving thumbnail", err)
		return
	}

	thumbnailPath := fmt.Sprintf("http://localhost:%v/%v", cfg.port, path)
	video.ThumbnailURL = &thumbnailPath

	err = cfg.db.UpdateVideo(video)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error updating video", err)
		return
	}

	respondWithJSON(w, http.StatusOK, video)
}
