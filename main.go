package main // executable program lives in package main

import ( // standard library imports only
	"encoding/json" // decode the listing JSON
	"errors"        // sentinel errors and errors.Is
	"fmt"           // formatted strings and errors
	"io"            // streaming and reading response bodies
	"log"           // timestamped console logging
	"math/rand"     // random jitter for retry delays
	"net/http"      // HTTP client
	"net/url"       // safe query-string building
	"os"            // files and folders
	"path/filepath" // safe file path handling
	"strconv"       // string <-> int conversions
	"strings"       // string trimming
	"time"          // delays, timeouts and elapsed-time measurement
)

const ( // fixed settings (edit here instead of passing arguments)
	listingEndpointURL     = "https://www.etsi.org/custom/standardssearch/data.php" // endpoint that returns the JSON listing
	pdfBaseURL             = "https://www.etsi.org/deliver/"                        // base address that PDF paths are appended to
	pdfDirectoryName       = "PDFs"                                                 // local folder where PDFs are saved
	firstListingPageNumber = 1                                                      // first listing page to read
	maxListingPagesToVisit = 0                                                      // listing page limit (0 = all pages, set e.g. 2 to test)
	delayBetweenRequests   = 300 * time.Millisecond                                 // pause after each network download
	listingRequestTimeout  = 30 * time.Second                                       // timeout for one listing request
	pdfDownloadTimeout     = 5 * time.Minute                                        // timeout for one PDF download (files can be large)
	maxRetryAttempts       = 5                                                      // retries per request before giving up
)

var errBlockedByServer = errors.New("blocked or rate limited by the server")     // server keeps answering 403/429
var errPermanentFailure = errors.New("permanent failure, retrying is pointless") // error that must not be retried

type StandardListingEntry struct { // the listing fields we actually need
	TotalCountText  string `json:"total_count"`      // total standards matching the query (arrives as text)
	Title           string `json:"TITLE"`            // human-readable standard title
	DeliverableName string `json:"ETSI_DELIVERABLE"` // deliverable name and version, e.g. "ETSI GR ZSM 021 V1.1.1"
	FolderPath      string `json:"EDSpathname"`      // folder path on the ETSI server
	PdfFilename     string `json:"EDSPDFfilename"`   // PDF file name
}

type RunStatistics struct { // counters reported at the end of the run
	PdfsDownloaded           int   // PDFs downloaded during this run
	PdfsSkippedAlreadyOnDisk int   // PDFs skipped because they already exist locally
	PdfsFailedToDownload     int   // PDFs that failed even after retries
	EntriesWithoutPdf        int   // listing entries that have no PDF
	TotalBytesDownloaded     int64 // total size of the PDFs downloaded during this run
}

func main() { // program entry point
	log.SetFlags(log.LstdFlags | log.Lmicroseconds) // timestamps with microseconds in every log line
	programStartTime := time.Now()                  // remember when we started, for the final elapsed time

	log.Printf("STARTUP: ETSI PDF downloader starting")                  // announce start
	log.Printf("STARTUP: listing endpoint     = %s", listingEndpointURL) // show settings
	log.Printf("STARTUP: PDF base URL         = %s", pdfBaseURL)
	log.Printf("STARTUP: output folder        = %s/", pdfDirectoryName)
	log.Printf("STARTUP: first listing page   = %d", firstListingPageNumber)
	log.Printf("STARTUP: max pages to visit   = %d (0 means all)", maxListingPagesToVisit)
	log.Printf("STARTUP: delay between calls  = %s", delayBetweenRequests)
	log.Printf("STARTUP: max retry attempts   = %d", maxRetryAttempts)

	listingHttpClient := &http.Client{Timeout: listingRequestTimeout} // client used for the listing pages
	pdfHttpClient := &http.Client{Timeout: pdfDownloadTimeout}        // client used for PDF downloads (longer timeout)

	log.Printf("STARTUP: making sure the folder %s/ exists", pdfDirectoryName) // say what we are doing
	createFolderError := os.MkdirAll(pdfDirectoryName, 0o755)                  // create PDFs/ if it does not exist
	if createFolderError != nil {                                              // folder could not be created
		log.Fatalf("STARTUP: cannot create folder %s: %v", pdfDirectoryName, createFolderError) // print the error and exit
	}
	log.Printf("STARTUP: folder %s/ is ready", pdfDirectoryName) // confirm the folder is ready

	statistics := RunStatistics{}                      // all counters start at zero
	currentListingPageNumber := firstListingPageNumber // listing page we are about to visit
	lastListingPageNumber := 0                         // last page to visit (unknown until the first page is read)

outerLoop: // label so we can stop both loops at once
	for { // loop over listing pages
		log.Printf("LISTING PAGE %d: visiting now", currentListingPageNumber)                                              // say which page we visit
		var listingEntries []StandardListingEntry                                                                          // rows found on the current page
		listingPageURL := listingEndpointURL + "?" + url.Values{"page": {strconv.Itoa(currentListingPageNumber)}}.Encode() // listing URL with ?page=N
		listingFetchStartTime := time.Now()                                                                                // measure how long the listing request takes

		listingFetchError := performGetRequestWithRetries( // fetch and decode the listing page
			listingHttpClient, // client to use
			listingPageURL,    // address to request
			fmt.Sprintf("listing page %d", currentListingPageNumber), // description used in log lines
			func(response *http.Response) error { // handler that reads the successful response
				responseBodyBytes, readError := io.ReadAll(io.LimitReader(response.Body, 32<<20)) // read the body, capped at 32 MB
				if readError != nil {                                                             // reading failed
					return readError // retryable error
				}
				log.Printf("LISTING PAGE %d: received %d bytes, decoding JSON", currentListingPageNumber, len(responseBodyBytes)) // log size
				return json.Unmarshal(responseBodyBytes, &listingEntries)                                                         // decode JSON (an HTML block page fails here and is retried)
			},
		)
		if listingFetchError != nil { // listing page failed even after retries
			log.Printf("LISTING PAGE %d: FAILED after retries, stopping the whole run: %v", currentListingPageNumber, listingFetchError) // report why we stop
			break outerLoop                                                                                                              // leave the page loop
		}
		log.Printf("LISTING PAGE %d: decoded %d entries in %s", currentListingPageNumber, len(listingEntries), time.Since(listingFetchStartTime).Round(time.Millisecond)) // report what we got

		if len(listingEntries) == 0 { // empty page means we ran past the data
			log.Printf("LISTING PAGE %d: is empty, we reached the end of the data", currentListingPageNumber) // report end of data
			break outerLoop                                                                                   // leave the page loop
		}

		if lastListingPageNumber == 0 { // first successful page: work out how many pages exist
			totalStandardsCount, _ := strconv.Atoi(listingEntries[0].TotalCountText)                                                                              // total standards reported by the API
			entriesPerPage := len(listingEntries)                                                                                                                 // rows per page (50 at the moment)
			lastListingPageNumber = (totalStandardsCount + entriesPerPage - 1) / entriesPerPage                                                                   // round up to get the last page number
			log.Printf("LISTING: API reports %d standards, %d per page, so the data ends at page %d", totalStandardsCount, entriesPerPage, lastListingPageNumber) // explain the math
			if maxListingPagesToVisit > 0 && firstListingPageNumber+maxListingPagesToVisit-1 < lastListingPageNumber {                                            // apply the optional page limit
				lastListingPageNumber = firstListingPageNumber + maxListingPagesToVisit - 1                        // cap at the configured limit
				log.Printf("LISTING: page limit applied, the last page to visit is now %d", lastListingPageNumber) // tell the user
			}
			log.Printf("LISTING: plan is to visit pages %d to %d", firstListingPageNumber, lastListingPageNumber) // show the plan
		}

		downloadedBeforeThisPage := statistics.PdfsDownloaded        // remember counters to report per-page numbers
		skippedBeforeThisPage := statistics.PdfsSkippedAlreadyOnDisk // remember skipped count
		failedBeforeThisPage := statistics.PdfsFailedToDownload      // remember failed count

		for entryIndexOnPage, listingEntry := range listingEntries { // go through each standard on this page
			entryPositionText := fmt.Sprintf("page %d entry %d/%d", currentListingPageNumber, entryIndexOnPage+1, len(listingEntries)) // e.g. "page 3 entry 7/50"

			if listingEntry.PdfFilename == "" || listingEntry.FolderPath == "" { // some entries have no PDF
				statistics.EntriesWithoutPdf++                                                                             // count it
				log.Printf("PDF [%s]: no PDF available for %q, skipping", entryPositionText, listingEntry.DeliverableName) // say why we skip
				continue                                                                                                   // nothing to download
			}

			pdfFilename := filepath.Base(listingEntry.PdfFilename)                                                 // strip any directory part from the file name
			localPdfPath := filepath.Join(pdfDirectoryName, pdfFilename)                                           // local destination: PDFs/<name>
			log.Printf("PDF [%s]: checking %s (%s)", entryPositionText, pdfFilename, listingEntry.DeliverableName) // say what we are checking

			existingFileInfo, fileCheckError := os.Stat(localPdfPath) // look for the file on disk
			if fileCheckError == nil && existingFileInfo.Size() > 0 { // file already downloaded
				statistics.PdfsSkippedAlreadyOnDisk++                                                                    // count it
				log.Printf("PDF [%s]: already on disk (%d bytes), skipping", entryPositionText, existingFileInfo.Size()) // say why we skip
				continue                                                                                                 // skip without touching the network
			}

			pdfDownloadURL := pdfBaseURL + strings.TrimSuffix(listingEntry.FolderPath, "/") + "/" + pdfFilename // deliver/<folder>/<file>.pdf
			log.Printf("PDF [%s]: not on disk yet, downloading from %s", entryPositionText, pdfDownloadURL)     // show the exact URL
			pdfDownloadStartTime := time.Now()                                                                  // measure the download time
			var bytesWrittenForThisPdf int64                                                                    // filled in by the handler below

			pdfDownloadError := performGetRequestWithRetries( // download with retries
				pdfHttpClient,                      // client to use
				pdfDownloadURL,                     // address to request
				fmt.Sprintf("PDF %s", pdfFilename), // description used in log lines
				func(response *http.Response) error { // handler that stores the successful response
					var saveError error                                                            // error from saving, if any
					bytesWrittenForThisPdf, saveError = savePdfToDisk(response.Body, localPdfPath) // stream the body into PDFs/<name>
					return saveError                                                               // hand the result back to the retry logic
				},
			)
			if pdfDownloadError != nil { // download failed even after retries
				if errors.Is(pdfDownloadError, errBlockedByServer) { // we are being blocked or rate limited
					log.Printf("PDF [%s]: the server is blocking us, stopping the whole run (rerun later to resume): %v", entryPositionText, pdfDownloadError) // report the limit
					break outerLoop                                                                                                                            // stop everything, rerun later to resume
				}
				statistics.PdfsFailedToDownload++                                                                                    // count the failure
				log.Printf("PDF [%s]: FAILED %s, moving on to the next entry: %v", entryPositionText, pdfFilename, pdfDownloadError) // report and continue
				continue                                                                                                             // move on to the next standard
			}

			statistics.PdfsDownloaded++                                                                                                                                                                                                    // count the success
			statistics.TotalBytesDownloaded += bytesWrittenForThisPdf                                                                                                                                                                      // add to the running size total
			log.Printf("PDF [%s]: SUCCESS saved %s (%d bytes) in %s. New PDFs this run: %d", entryPositionText, localPdfPath, bytesWrittenForThisPdf, time.Since(pdfDownloadStartTime).Round(time.Millisecond), statistics.PdfsDownloaded) // report success
			log.Printf("PDF [%s]: waiting %s before the next request", entryPositionText, delayBetweenRequests)                                                                                                                            // say why we pause
			time.Sleep(delayBetweenRequests)                                                                                                                                                                                               // be polite to the server
		}

		log.Printf("LISTING PAGE %d/%d: finished. This page: downloaded=%d skipped=%d failed=%d. Run totals: downloaded=%d skipped=%d failed=%d no-pdf=%d", // per-page summary
			currentListingPageNumber, lastListingPageNumber,
			statistics.PdfsDownloaded-downloadedBeforeThisPage,
			statistics.PdfsSkippedAlreadyOnDisk-skippedBeforeThisPage,
			statistics.PdfsFailedToDownload-failedBeforeThisPage,
			statistics.PdfsDownloaded, statistics.PdfsSkippedAlreadyOnDisk, statistics.PdfsFailedToDownload, statistics.EntriesWithoutPdf)

		if currentListingPageNumber >= lastListingPageNumber { // reached the last page (or the configured limit)
			log.Printf("LISTING: reached the last page (%d), no more pages to visit", lastListingPageNumber) // explain why we stop
			break                                                                                            // all done
		}
		currentListingPageNumber++                                                                                   // move to the next listing page
		log.Printf("LISTING: moving on to page %d after a %s pause", currentListingPageNumber, delayBetweenRequests) // say what comes next
		time.Sleep(delayBetweenRequests)                                                                             // pause between listing requests
	}

	log.Printf("SUMMARY: downloaded=%d skipped-already-on-disk=%d failed=%d entries-without-pdf=%d", statistics.PdfsDownloaded, statistics.PdfsSkippedAlreadyOnDisk, statistics.PdfsFailedToDownload, statistics.EntriesWithoutPdf) // final counters
	log.Printf("SUMMARY: downloaded %.1f MB this run in %s", float64(statistics.TotalBytesDownloaded)/(1024*1024), time.Since(programStartTime).Round(time.Second))                                                                 // final size and elapsed time
}

// savePdfToDisk streams a response body to destinationFilePath, but only if it really is a PDF.
func savePdfToDisk(responseBody io.Reader, destinationFilePath string) (int64, error) { // returns the number of bytes written
	pdfSignatureBytes := make([]byte, 5)                                  // every PDF starts with the 5 characters "%PDF-"
	_, signatureReadError := io.ReadFull(responseBody, pdfSignatureBytes) // read the first 5 bytes
	if signatureReadError != nil {                                        // empty or broken response
		return 0, fmt.Errorf("could not read the first bytes of the response: %w", signatureReadError) // retryable error
	}
	if string(pdfSignatureBytes) != "%PDF-" { // not a PDF (e.g. an HTML error page)
		log.Printf("SAVE: response for %s does not start with %%PDF- (starts with %q), discarding it", destinationFilePath, pdfSignatureBytes) // explain the rejection
		return 0, fmt.Errorf("response is not a PDF: %w", errPermanentFailure)                                                                 // do not keep it, do not retry
	}
	log.Printf("SAVE: signature %%PDF- confirmed, writing to %s", destinationFilePath) // confirm it is a PDF

	temporaryFilePath := destinationFilePath + ".part"             // temp name so half-downloaded files never look complete
	temporaryFile, createFileError := os.Create(temporaryFilePath) // create (or truncate) the temp file
	if createFileError != nil {                                    // could not create the file
		return 0, fmt.Errorf("%v: %w", createFileError, errPermanentFailure) // local problem, do not retry
	}
	log.Printf("SAVE: created temporary file %s", temporaryFilePath) // say where the data goes first

	totalBytesWritten := int64(len(pdfSignatureBytes))               // we already have the 5 signature bytes
	_, writeSignatureError := temporaryFile.Write(pdfSignatureBytes) // write the 5 bytes we already read
	var copyError error                                              // error from copying the rest
	if writeSignatureError == nil {                                  // signature written fine
		var restOfFileBytes int64                                         // size of the rest of the PDF
		restOfFileBytes, copyError = io.Copy(temporaryFile, responseBody) // stream the rest of the PDF to disk
		totalBytesWritten += restOfFileBytes                              // add to the total
	} else { // writing the signature failed
		copyError = writeSignatureError // report that error instead
	}
	closeFileError := temporaryFile.Close() // close the file (also flushes to disk)
	if copyError == nil {                   // keep the first error only
		copyError = closeFileError // surface a close error if nothing else failed
	}
	if copyError != nil { // something went wrong while writing
		log.Printf("SAVE: writing %s failed (%v), deleting the partial file", temporaryFilePath, copyError) // explain the cleanup
		os.Remove(temporaryFilePath)                                                                        // delete the partial file
		return 0, copyError                                                                                 // retryable error
	}

	renameError := os.Rename(temporaryFilePath, destinationFilePath) // atomically move into place: now it counts as downloaded
	if renameError != nil {                                          // rename failed
		return 0, fmt.Errorf("%v: %w", renameError, errPermanentFailure) // local problem, do not retry
	}
	log.Printf("SAVE: renamed %s to %s (%d bytes)", temporaryFilePath, destinationFilePath, totalBytesWritten) // confirm completion
	return totalBytesWritten, nil                                                                              // success
}

// performGetRequestWithRetries performs a GET and passes a 200 response to handleSuccessfulResponse, retrying transient failures.
func performGetRequestWithRetries(httpClient *http.Client, requestURL string, requestDescription string, handleSuccessfulResponse func(*http.Response) error) error { // generic GET with retries
	totalAttemptsAllowed := maxRetryAttempts + 1 // the first try plus the retries
	lastHttpStatusCode := 0                      // last HTTP status seen, for error reporting

	for attemptNumber := 1; ; attemptNumber++ { // try until success or attempts are used up
		log.Printf("HTTP: %s, attempt %d of %d: GET %s", requestDescription, attemptNumber, totalAttemptsAllowed, requestURL) // say what we request
		attemptStartTime := time.Now()                                                                                        // measure how long this attempt takes

		httpRequest, buildRequestError := http.NewRequest(http.MethodGet, requestURL, nil) // build the GET request
		if buildRequestError != nil {                                                      // request could not be built
			return fmt.Errorf("%v: %w", buildRequestError, errPermanentFailure) // no point retrying
		}
		httpRequest.Header.Set("User-Agent", "etsi-standards-fetcher/1.0 (personal research)") // identify ourselves

		var waitBeforeNextAttempt time.Duration                      // how long to wait before the next attempt
		var attemptError error                                       // reason this attempt failed, if it did
		httpResponse, sendRequestError := httpClient.Do(httpRequest) // send the request
		if sendRequestError != nil {                                 // network-level failure (timeout, DNS, connection reset)
			attemptError = sendRequestError                                                                                                                                           // remember the reason
			log.Printf("HTTP: %s, attempt %d: network error after %s: %v", requestDescription, attemptNumber, time.Since(attemptStartTime).Round(time.Millisecond), sendRequestError) // log it
		} else { // we got an HTTP response (any status)
			lastHttpStatusCode = httpResponse.StatusCode                                                                                                                                                                                           // remember the status code
			log.Printf("HTTP: %s, attempt %d: status %d, content length %d, headers received in %s", requestDescription, attemptNumber, httpResponse.StatusCode, httpResponse.ContentLength, time.Since(attemptStartTime).Round(time.Millisecond)) // log the response line

			switch { // decide what to do based on the status
			case httpResponse.StatusCode == http.StatusOK: // success status
				attemptError = handleSuccessfulResponse(httpResponse) // let the caller consume the body
				httpResponse.Body.Close()                             // always close the body
				if attemptError == nil {                              // handler succeeded
					log.Printf("HTTP: %s, attempt %d: success in %s", requestDescription, attemptNumber, time.Since(attemptStartTime).Round(time.Millisecond)) // log success
					return nil                                                                                                                                 // done
				}
				if errors.Is(attemptError, errPermanentFailure) { // handler says do not retry
					log.Printf("HTTP: %s: permanent failure, not retrying: %v", requestDescription, attemptError) // explain
					return attemptError                                                                           // give up immediately
				}
				log.Printf("HTTP: %s, attempt %d: response could not be processed: %v", requestDescription, attemptNumber, attemptError) // log the processing problem
			case httpResponse.StatusCode == 403 || httpResponse.StatusCode == 429 || httpResponse.StatusCode >= 500: // blocked, rate limited, or server error
				httpResponse.Body.Close()                                                                       // close the unused body
				attemptError = fmt.Errorf("HTTP status %d", httpResponse.StatusCode)                            // retryable error
				retryAfterSeconds, parseRetryAfterError := strconv.Atoi(httpResponse.Header.Get("Retry-After")) // server may tell us how long to wait
				if parseRetryAfterError == nil {                                                                // header was present and numeric
					waitBeforeNextAttempt = time.Duration(retryAfterSeconds) * time.Second                                             // honor Retry-After
					log.Printf("HTTP: %s: server asked us to wait %s (Retry-After header)", requestDescription, waitBeforeNextAttempt) // log it
				}
			default: // any other status (404, 400, ...) is permanent
				httpResponse.Body.Close()                                                                                 // close the unused body
				log.Printf("HTTP: %s: status %d is permanent, not retrying", requestDescription, httpResponse.StatusCode) // explain
				return fmt.Errorf("HTTP status %d: %w", httpResponse.StatusCode, errPermanentFailure)                     // no retry
			}
		}

		if attemptNumber >= totalAttemptsAllowed { // all attempts are used up
			log.Printf("RETRY: %s: no attempts left (%d used)", requestDescription, attemptNumber) // say we give up
			if lastHttpStatusCode == 403 || lastHttpStatusCode == 429 {                            // we are being blocked or rate limited
				return fmt.Errorf("%s: %w (HTTP status %d)", requestURL, errBlockedByServer, lastHttpStatusCode) // the "hit a limit" case
			}
			return fmt.Errorf("%s: giving up after %d attempts: %w", requestURL, attemptNumber, attemptError) // other failure
		}
		if waitBeforeNextAttempt == 0 { // no Retry-After header was given
			waitBeforeNextAttempt = time.Second << (attemptNumber - 1) // exponential backoff: 1s, 2s, 4s, 8s, 16s
		}
		randomJitter := time.Duration(rand.Intn(500)) * time.Millisecond                                                                                                                               // up to 0.5s of random jitter
		waitBeforeNextAttempt += randomJitter                                                                                                                                                          // add the jitter
		log.Printf("RETRY: %s: attempt %d failed (%v), waiting %s before attempt %d", requestDescription, attemptNumber, attemptError, waitBeforeNextAttempt.Round(time.Millisecond), attemptNumber+1) // explain the wait
		time.Sleep(waitBeforeNextAttempt)                                                                                                                                                              // wait before the next attempt
	}
}
