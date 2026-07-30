package com.syncmedia.app;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.app.Service;
import android.content.Intent;
import android.os.Build;
import android.os.IBinder;
import android.util.Log;
import java.io.File;
import java.io.IOException;
import java.util.Map;
import java.util.function.Supplier;

public class SyncService extends Service {

    private static final String TAG = "SyncMedia";
    private static final String CHANNEL_ID = "syncmedia_channel";
    private static final int NOTIFICATION_ID = 1;

    // Go 子进程单实例守卫（monitor 线程 + 在册进程 + 运行标志）
    private final ProcessGuard guard = new ProcessGuard();

    @Override
    public void onCreate() {
        super.onCreate();
        createNotificationChannel();
    }

    @Override
    public int onStartCommand(Intent intent, int flags, int startId) {
        // startForeground 每次必须照常调用（startForegroundService 的硬性要求）
        Notification notification = buildNotification("SyncMedia 启动中...");
        startForeground(NOTIFICATION_ID, notification);

        startGoBinary();

        return START_STICKY;
    }

    private void startGoBinary() {
        String nativeDir = getApplicationInfo().nativeLibraryDir;
        String binaryPath = nativeDir + "/libsyncmedia.so";

        Log.i(TAG, "二进制路径: " + binaryPath);
        File binary = new File(binaryPath);
        if (!binary.exists()) {
            Log.e(TAG, "二进制不存在");
            updateNotification("错误: 二进制不存在");
            return;
        }

        try {
            binary.setExecutable(true);
        } catch (Exception e) {
            Log.e(TAG, "设置执行权限失败", e);
        }

        // 启动监控线程（含自动重启）；monitor 存活时幂等跳过
        if (!guard.tryBegin(() -> new Thread(() -> monitorLoop(binaryPath)))) {
            Log.i(TAG, "已在运行，跳过重复启动");
        }
    }

    // 监控循环：启动 Go 子进程、转发输出、退出后 5 秒自动重启
    private void monitorLoop(String binaryPath) {
        while (guard.shouldContinue()) {
            try {
                ProcessBuilder pb = new ProcessBuilder(
                    binaryPath, "start",
                    "--tunnel", "bore",
                    "--port", "8999",
                    "--web-port", "8080"
                );

                // 关键：Android 没有 /etc/resolv.conf，
                // 必须用 Go 纯 DNS 解析器
                Map<String, String> env = pb.environment();
                env.put("GODEBUG", "netdns=go");
                env.put("GOTRACEBACK", "single");

                pb.redirectErrorStream(true);
                Process p = pb.start();

                // stop 竞态封堵：守卫已停止则立刻销毁刚启动的进程
                if (!guard.register(p)) {
                    p.destroy();
                    break;
                }

                Log.i(TAG, "Go 服务已启动, PID: " + p.pid());
                updateNotification("SyncMedia 运行中");

                try {
                    // 读取输出
                    byte[] buf = new byte[2048];
                    int n;
                    while ((n = p.getInputStream().read(buf)) != -1) {
                        String line = new String(buf, 0, n).trim();
                        if (!line.isEmpty()) {
                            Log.i(TAG, line);
                        }
                    }

                    // 进程退出
                    int exitCode = p.waitFor();
                    Log.w(TAG, "Go 进程退出, code=" + exitCode);
                } finally {
                    guard.unregister(p);
                }

            } catch (IOException e) {
                Log.e(TAG, "启动失败: " + e.getMessage());
            } catch (InterruptedException e) {
                break;
            }

            if (guard.shouldContinue()) {
                updateNotification("SyncMedia 重启中...");
                Log.i(TAG, "5 秒后重启...");
                try {
                    Thread.sleep(5000);
                } catch (InterruptedException e) {
                    break;
                }
            }
        }
    }

    private void updateNotification(String text) {
        NotificationManager mgr = getSystemService(NotificationManager.class);
        if (mgr != null) {
            mgr.notify(NOTIFICATION_ID, buildNotification(text));
        }
    }

    private Notification buildNotification(String text) {
        Intent notifIntent = new Intent(this, MainActivity.class);
        PendingIntent pendingIntent = PendingIntent.getActivity(
            this, 0, notifIntent,
            PendingIntent.FLAG_UPDATE_CURRENT | PendingIntent.FLAG_IMMUTABLE
        );

        Notification notification = new Notification.Builder(this, CHANNEL_ID)
            .setContentTitle("SyncMedia")
            .setContentText(text)
            .setSmallIcon(android.R.drawable.ic_media_play)
            .setContentIntent(pendingIntent)
            .setOngoing(true)
            .build();

        return notification;
    }

    private void createNotificationChannel() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            NotificationChannel channel = new NotificationChannel(
                CHANNEL_ID, "SyncMedia 服务", NotificationManager.IMPORTANCE_LOW
            );
            channel.setDescription("同步观影服务");
            NotificationManager mgr = getSystemService(NotificationManager.class);
            if (mgr != null) {
                mgr.createNotificationChannel(channel);
            }
        }
    }

    @Override
    public void onDestroy() {
        super.onDestroy();
        guard.stop();
        Log.i(TAG, "Go 服务已停止");
    }

    @Override
    public IBinder onBind(Intent intent) {
        return null;
    }
}

/**
 * Go 子进程监护的单实例守卫（纯 Java，无 Android 依赖，可在宿主 JVM 直接测试）。
 * 不变量：至多一个存活 monitor 线程，monitor 内至多一个在册进程；
 * stop 之后 register 恒为 false，不会再有新进程逃逸。
 * 全部状态由 synchronized 保护。
 */
final class ProcessGuard {

    private Thread monitor;
    private Process current;
    private boolean shouldRun;

    // 原子 check-and-act：monitor 存活则返回 false（幂等，不重复拉起）；
    // 否则置 shouldRun 并启动 factory 创建的新 monitor
    synchronized boolean tryBegin(Supplier<Thread> factory) {
        if (monitor != null && monitor.isAlive()) {
            return false;
        }
        shouldRun = true;
        monitor = factory.get();
        monitor.start();
        return true;
    }

    // monitor 循环条件与重启判断统一走这里
    synchronized boolean shouldContinue() {
        return shouldRun;
    }

    // 登记新启动的子进程；返回 false 表示已 stop，
    // 调用方必须立刻 destroy 该进程（堵住 stop 竞态）
    synchronized boolean register(Process p) {
        if (!shouldRun) {
            return false;
        }
        current = p;
        return true;
    }

    // 子进程退出后注销；仅当仍是在册进程时清除
    synchronized void unregister(Process p) {
        if (current == p) {
            current = null;
        }
    }

    // 停止：临界区内置 shouldRun=false 并取出引用，
    // 临界区外中断 monitor、销毁在册进程（避免持锁做外部调用）
    void stop() {
        Thread m;
        Process p;
        synchronized (this) {
            shouldRun = false;
            m = monitor;
            p = current;
        }
        if (m != null) {
            m.interrupt();
        }
        if (p != null) {
            p.destroy();
        }
    }
}
