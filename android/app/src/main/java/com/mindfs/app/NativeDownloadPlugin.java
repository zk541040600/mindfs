package com.mindfs.app;

import android.app.DownloadManager;
import android.content.ContentResolver;
import android.content.ContentValues;
import android.content.Context;
import android.net.Uri;
import android.os.Build;
import android.os.Environment;
import android.provider.MediaStore;
import android.text.TextUtils;
import android.util.Base64;
import android.webkit.CookieManager;
import android.webkit.URLUtil;
import com.getcapacitor.JSObject;
import com.getcapacitor.Plugin;
import com.getcapacitor.PluginCall;
import com.getcapacitor.PluginMethod;
import com.getcapacitor.annotation.CapacitorPlugin;
import java.io.File;
import java.io.FileOutputStream;
import java.io.OutputStream;

@CapacitorPlugin(name = "NativeDownload")
public class NativeDownloadPlugin extends Plugin {
    @PluginMethod
    public void download(PluginCall call) {
        String url = call.getString("url");
        String filename = sanitizeFilename(call.getString("filename"));

        if (TextUtils.isEmpty(url)) {
            call.reject("url is required");
            return;
        }

        if (TextUtils.isEmpty(filename)) {
            filename = sanitizeFilename(URLUtil.guessFileName(url, null, null));
        }
        if (TextUtils.isEmpty(filename)) {
            filename = "download";
        }

        DownloadManager downloadManager =
            (DownloadManager) getContext().getSystemService(Context.DOWNLOAD_SERVICE);
        if (downloadManager == null) {
            call.reject("DownloadManager is unavailable");
            return;
        }

        try {
            long downloadId = enqueueDownload(downloadManager, url, filename);

            JSObject result = new JSObject();
            result.put("downloadId", downloadId);
            result.put("filename", filename);
            result.put("directory", Environment.DIRECTORY_DOWNLOADS);
            call.resolve(result);
        } catch (Exception ex) {
            call.reject("Failed to enqueue download: " + ex.getMessage(), ex);
        }
    }

    @PluginMethod
    public void saveBase64(PluginCall call) {
        String data = call.getString("data");
        String filename = sanitizeFilename(call.getString("filename"));
        if (TextUtils.isEmpty(data)) {
            call.reject("data is required");
            return;
        }
        if (TextUtils.isEmpty(filename)) {
            filename = "download";
        }
        try {
            Uri uri = saveBase64ToDownloads(getContext(), data, filename);
            JSObject result = new JSObject();
            result.put("filename", filename);
            result.put("directory", Environment.DIRECTORY_DOWNLOADS);
            result.put("uri", uri.toString());
            call.resolve(result);
        } catch (Exception ex) {
            call.reject("Failed to save download: " + ex.getMessage(), ex);
        }
    }

    static Uri saveBase64ToDownloads(Context context, String data, String filename) throws Exception {
        byte[] bytes = Base64.decode(data, Base64.DEFAULT);
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            ContentResolver resolver = context.getContentResolver();
            ContentValues values = new ContentValues();
            values.put(MediaStore.MediaColumns.DISPLAY_NAME, filename);
            values.put(MediaStore.MediaColumns.MIME_TYPE, "application/octet-stream");
            values.put(MediaStore.MediaColumns.RELATIVE_PATH, Environment.DIRECTORY_DOWNLOADS);
            values.put(MediaStore.MediaColumns.IS_PENDING, 1);
            Uri uri = resolver.insert(MediaStore.Downloads.EXTERNAL_CONTENT_URI, values);
            if (uri == null) {
                throw new IllegalStateException("failed to create download entry");
            }
            try (OutputStream out = resolver.openOutputStream(uri)) {
                if (out == null) {
                    throw new IllegalStateException("failed to open download entry");
                }
                out.write(bytes);
            } catch (Exception ex) {
                resolver.delete(uri, null, null);
                throw ex;
            }
            ContentValues done = new ContentValues();
            done.put(MediaStore.MediaColumns.IS_PENDING, 0);
            resolver.update(uri, done, null, null);
            return uri;
        }

        File downloadsDir = Environment.getExternalStoragePublicDirectory(Environment.DIRECTORY_DOWNLOADS);
        try {
            return writeBytesToUniqueFile(downloadsDir, filename, bytes);
        } catch (Exception publicWriteError) {
            File appDownloadsDir = context.getExternalFilesDir(Environment.DIRECTORY_DOWNLOADS);
            if (appDownloadsDir == null) {
                throw publicWriteError;
            }
            return writeBytesToUniqueFile(appDownloadsDir, filename, bytes);
        }
    }

    private static Uri writeBytesToUniqueFile(File directory, String filename, byte[] bytes) throws Exception {
        if (!directory.exists() && !directory.mkdirs()) {
            throw new IllegalStateException("failed to create downloads directory");
        }
        File target = uniqueDownloadFile(directory, filename);
        try (OutputStream out = new FileOutputStream(target)) {
            out.write(bytes);
        }
        return Uri.fromFile(target);
    }

    private static File uniqueDownloadFile(File directory, String filename) {
        File candidate = new File(directory, filename);
        int dotIndex = filename.lastIndexOf('.');
        String base = dotIndex > 0 ? filename.substring(0, dotIndex) : filename;
        String ext = dotIndex > 0 ? filename.substring(dotIndex) : "";
        for (int index = 1; candidate.exists(); index += 1) {
            candidate = new File(directory, base + " (" + index + ")" + ext);
        }
        return candidate;
    }

    static long enqueueDownload(DownloadManager downloadManager, String url, String filename) {
        DownloadManager.Request request = new DownloadManager.Request(Uri.parse(url));
        request.setTitle(filename);
        request.setDescription("Downloading file");
        request.setNotificationVisibility(
            DownloadManager.Request.VISIBILITY_VISIBLE_NOTIFY_COMPLETED
        );
        request.setAllowedOverMetered(true);
        request.setAllowedOverRoaming(true);
        request.setVisibleInDownloadsUi(true);
        if (filename.toLowerCase().endsWith(".apk")) {
            request.setMimeType("application/vnd.android.package-archive");
            request.setDescription("Downloading app update");
        }
        request.setDestinationInExternalPublicDir(
            Environment.DIRECTORY_DOWNLOADS,
            filename
        );
        String cookies = CookieManager.getInstance().getCookie(url);
        if (!TextUtils.isEmpty(cookies)) {
            request.addRequestHeader("Cookie", cookies);
        }

        return downloadManager.enqueue(request);
    }

    static String sanitizeFilename(String filename) {
        if (filename == null) {
            return "";
        }
        String sanitized = filename
            .trim()
            .replace("\\", "_")
            .replace("/", "_")
            .replace(":", "_")
            .replace("*", "_")
            .replace("?", "_")
            .replace("\"", "_")
            .replace("<", "_")
            .replace(">", "_")
            .replace("|", "_")
            .replaceAll("[\\x00-\\x1f\\x7f]", "_");
        if (sanitized.equals(".") || sanitized.equals("..")) {
            return "";
        }
        return sanitized;
    }
}
