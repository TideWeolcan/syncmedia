package com.syncmedia.app;

import android.app.Activity;
import android.content.Intent;
import android.content.pm.PackageManager;
import android.graphics.Color;
import android.os.Build;
import android.os.Bundle;
import android.os.Handler;
import android.os.Looper;
import android.view.Gravity;
import android.webkit.WebResourceError;
import android.webkit.WebResourceRequest;
import android.webkit.WebSettings;
import android.webkit.WebView;
import android.webkit.WebViewClient;
import android.widget.FrameLayout;
import android.widget.TextView;

public class MainActivity extends Activity {

    private WebView webView;
    private TextView loadingText;
    private Handler handler = new Handler(Looper.getMainLooper());
    private int retryCount = 0;
    private static final int MAX_RETRIES = 20;
    private static final int RETRY_DELAY = 1500;

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);

        // 单一 FrameLayout：WebView 底层 + 加载提示顶层，永不切换 contentView
        FrameLayout root = new FrameLayout(this);
        root.setBackgroundColor(Color.parseColor("#0f172a"));

        // WebView（底层）
        webView = new WebView(this);
        WebSettings s = webView.getSettings();
        s.setJavaScriptEnabled(true);
        s.setDomStorageEnabled(true);
        s.setCacheMode(WebSettings.LOAD_NO_CACHE);

        webView.setWebViewClient(new WebViewClient() {
            @Override
            public boolean shouldOverrideUrlLoading(WebView v, WebResourceRequest req) {
                return false;
            }

            @Override
            public void onPageFinished(WebView v, String url) {
                loadingText.setVisibility(android.view.View.GONE);
            }

            @Override
            public void onReceivedError(WebView v, WebResourceRequest req, WebResourceError err) {
                retryCount++;
                if (retryCount <= MAX_RETRIES) {
                    loadingText.setText("等待服务启动... (" + retryCount + "/" + MAX_RETRIES + ")");
                    loadingText.setVisibility(android.view.View.VISIBLE);
                    handler.postDelayed(() -> {
                        if (!isFinishing()) webView.loadUrl("http://127.0.0.1:8080");
                    }, RETRY_DELAY);
                } else {
                    loadingText.setText("服务未就绪\n请确保 app 有网络权限");
                }
            }
        });

        FrameLayout.LayoutParams webParams = new FrameLayout.LayoutParams(
            FrameLayout.LayoutParams.MATCH_PARENT,
            FrameLayout.LayoutParams.MATCH_PARENT);
        root.addView(webView, webParams);

        // 加载提示（顶层）
        loadingText = new TextView(this);
        loadingText.setText("SyncMedia 启动中...");
        loadingText.setTextColor(Color.parseColor("#38bdf8"));
        loadingText.setTextSize(16);
        loadingText.setGravity(Gravity.CENTER);
        loadingText.setPadding(48, 48, 48, 48);

        FrameLayout.LayoutParams textParams = new FrameLayout.LayoutParams(
            FrameLayout.LayoutParams.WRAP_CONTENT,
            FrameLayout.LayoutParams.WRAP_CONTENT);
        textParams.gravity = Gravity.CENTER;
        root.addView(loadingText, textParams);

        setContentView(root);

        // 启动前台服务
        try {
            Intent svc = new Intent(this, SyncService.class);
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
                startForegroundService(svc);
            } else {
                startService(svc);
            }
        } catch (Exception e) {
            loadingText.setText("无法启动服务: " + e.getMessage());
        }

        // Android 13+ 通知权限
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            if (checkSelfPermission(android.Manifest.permission.POST_NOTIFICATIONS)
                    != PackageManager.PERMISSION_GRANTED) {
                requestPermissions(
                    new String[]{android.Manifest.permission.POST_NOTIFICATIONS}, 1);
            }
        }

        // 延迟首次加载
        handler.postDelayed(() -> {
            if (!isFinishing()) webView.loadUrl("http://127.0.0.1:8080");
        }, 2000);
    }

    @Override
    public void onBackPressed() {
        if (webView != null && webView.canGoBack()) {
            webView.goBack();
        } else {
            super.onBackPressed();
        }
    }

    @Override
    protected void onDestroy() {
        handler.removeCallbacksAndMessages(null);
        super.onDestroy();
    }
}
