# Text Extraction
This is an application with input a pdf file and output the text of the pdf file.

### Components
- producer (Azure function app)
- pdf_queue (Service Bus queue)
- pdf-storage (Blob storage container)
- ocr_container (Azure container app job)
- text-storage (Blob storage container)
- consumer (Azure function app)

### Code components
- producer: Takes a pdf from an http request and tries to extract text from all pages. If it cannot extract text from at least one page it stores the pdf in pdf-storage container and sends a message to the pdf_queue queue. If it can extract text from all the pages then it stores the text in text-storage container. It returns a uuid to the user that they can use to get the response from the consumer.
- consumer: When it gets an http request with a valid uuid, it checks the text-storage container and if it has the text it returns it. Otherwise returns that it does not exist. The text is automatically removed from the text-storage after 1 day.
- ocr_container: It gets messages from the pdf_queue and the pdf from the pdf-storage and does text extraction from the text layer if present, or ocr when the text layer is not present. It stores the extracted text in the text-storage and deletes the pdf from the pdf_store.

### Implementation details
producer, consumer and ocr_container are all written in Go. Go do not need a custom handler.

### OCR
The ocr_container has threads that perform ocr in parallel in the pages of the pdfs. If one page is not clear enough for the ocr to read, it skips it. Tesseract with eng+ell languages.

### API contract
Producer pdf size limit 90MB
Consumer output format: json with per-page text. 

### Security
Use managed identity for Blob and Service Bus instead of connection strings.

### Ops
- local dev (Azurite plus the Service Bus emulator).


