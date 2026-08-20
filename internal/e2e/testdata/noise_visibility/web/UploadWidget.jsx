function UploadWidget({ onUpload }) {
  return (
    <div className="upload-widget">
      <button className="btn" onClick={handleUpload}>Upload</button>
    </div>
  );
}

function handleUpload() {
  return startMultipartUpload();
}

function startMultipartUpload() {
  return completeMultipartUpload();
}

function completeMultipartUpload() {
  return true;
}
