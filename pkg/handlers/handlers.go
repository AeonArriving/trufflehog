package handlers

import (
	"context"
	"io"

	diskbufferreader "github.com/bill-rich/disk-buffer-reader"

	logContext "github.com/trufflesecurity/trufflehog/v3/pkg/context"
	"github.com/trufflesecurity/trufflehog/v3/pkg/sources"
)

func DefaultHandlers() []Handler {
	return []Handler{
		&Archive{},
	}
}

// SpecializedHandler defines the interface for handlers that can process specialized archives.
// It includes a method to handle specialized archives and determine if the file is of a special type.
type SpecializedHandler interface {
	// HandleSpecialized examines the provided file reader within the context and determines if it is a specialized archive.
	// It returns a reader with any necessary modifications, a boolean indicating if the file was specialized,
	// and an error if something went wrong during processing.
	HandleSpecialized(logContext.Context, io.Reader) (io.Reader, bool, error)
}

type Handler interface {
	FromFile(context.Context, io.Reader) chan []byte
	IsFiletype(context.Context, io.Reader) (io.Reader, bool)
	New()
}

// HandleFile processes a given file by selecting an appropriate handler from DefaultHandlers.
// It first checks if the handler implements SpecializedHandler for any special processing,
// then falls back to regular file type handling. If successful, it reads the file in chunks,
// packages them in the provided chunk skeleton, and sends them to chunksChan.
// The function returns true if processing was successful and false otherwise.
// Context is used for cancellation, and the caller is responsible for canceling it if needed.
func HandleFile(ctx context.Context, file io.Reader, chunkSkel *sources.Chunk, chunksChan chan *sources.Chunk) bool {
	aCtx := logContext.AddLogger(ctx)
	for _, h := range DefaultHandlers() {
		h.New()

		// The re-reader is used to reset the file reader after checking if the handler implements SpecializedHandler.
		// This is necessary because the archive pkg doesn't correctly determine the file type when using
		// an io.MultiReader, which is used by the SpecializedHandler.
		reReader, err := diskbufferreader.New(file)
		if err != nil {
			aCtx.Logger().Error(err, "error creating reusable reader")
			return false
		}

		if success := processHandler(aCtx, h, reReader, chunkSkel, chunksChan); success {
			return true
		}
	}

	return false
}

func processHandler(ctx logContext.Context, h Handler, reReader *diskbufferreader.DiskBufferReader, chunkSkel *sources.Chunk, chunksChan chan *sources.Chunk) bool {
	defer reReader.Close()
	defer reReader.Stop()

	if specialHandler, ok := h.(SpecializedHandler); ok {
		file, isSpecial, err := specialHandler.HandleSpecialized(ctx, reReader)
		if isSpecial {
			return handleChunks(ctx, h.FromFile(ctx, file), chunkSkel, chunksChan)
		}
		if err != nil {
			ctx.Logger().Error(err, "error handling file")
		}
	}

	if _, err := reReader.Seek(0, io.SeekStart); err != nil {
		ctx.Logger().Error(err, "error seeking to start of file")
		return false
	}

	if _, isType := h.IsFiletype(ctx, reReader); !isType {
		return false
	}

	return handleChunks(ctx, h.FromFile(ctx, reReader), chunkSkel, chunksChan)
}

func handleChunks(ctx context.Context, handlerChan chan []byte, chunkSkel *sources.Chunk, chunksChan chan *sources.Chunk) bool {
	for {
		select {
		case data, open := <-handlerChan:
			if !open {
				return true
			}
			chunk := *chunkSkel
			chunk.Data = data
			select {
			case chunksChan <- &chunk:
			case <-ctx.Done():
				return false
			}
		case <-ctx.Done():
			return false
		}
	}
}
