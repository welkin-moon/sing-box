package io.nekohasekai.sfa.compose

import android.app.Activity
import android.content.Intent
import android.os.Bundle
import android.webkit.JavascriptInterface
import android.webkit.ValueCallback
import android.webkit.WebChromeClient
import android.webkit.WebResourceRequest
import android.webkit.WebSettings
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.Button
import android.widget.LinearLayout
import android.widget.Toast
import androidx.core.content.FileProvider
import java.io.File

/** Hosts only a bundled offline editor; exposes no remote JavaScript interface. */
class BondBuilderActivity : Activity() {
    private var chooser: ValueCallback<Array<android.net.Uri>>? = null
    private var pendingDocument: String? = null
    private lateinit var web: WebView
    override fun onCreate(state: Bundle?) {
        super.onCreate(state)
        val root = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL }
        root.addView(Button(this).apply {
            text = "代理连接和配置"
            setOnClickListener { startActivity(Intent(this@BondBuilderActivity, MainActivity::class.java)) }
        })
        web = WebView(this).apply {
            settings.javaScriptEnabled = true
            settings.allowFileAccess = false
            settings.allowContentAccess = true
            settings.domStorageEnabled = false
            settings.mixedContentMode = WebSettings.MIXED_CONTENT_NEVER_ALLOW
            webViewClient = object : WebViewClient() {
                override fun shouldOverrideUrlLoading(view: WebView?, request: WebResourceRequest?) = true
            }
            webChromeClient = object : WebChromeClient() {
                override fun onShowFileChooser(view: WebView?, callback: ValueCallback<Array<android.net.Uri>>, params: FileChooserParams?): Boolean {
                    chooser?.onReceiveValue(null)
                    chooser = callback
                    startActivityForResult(Intent(Intent.ACTION_OPEN_DOCUMENT).apply { type = "application/json"; addCategory(Intent.CATEGORY_OPENABLE) }, 41)
                    return true
                }
            }
            addJavascriptInterface(Host(), "PUKHost")
        }
        root.addView(web, LinearLayout.LayoutParams(-1, 0, 1f))
        setContentView(root)
        web.loadUrl("file:///android_asset/puk-bond.html")
    }
    inner class Host {
        @JavascriptInterface fun save(name: String, json: String) {
            if (json.length > 2 * 1024 * 1024) return
            val safeName = name.substringAfterLast('/').replace(Regex("[^A-Za-z0-9_.-]"), "_")
            runOnUiThread {
                pendingDocument = json
                startActivityForResult(Intent(Intent.ACTION_CREATE_DOCUMENT).apply { type = "application/json"; addCategory(Intent.CATEGORY_OPENABLE); putExtra(Intent.EXTRA_TITLE, safeName) }, 42)
            }
        }
        @JavascriptInterface fun install(json: String) {
            if (json.length > 2 * 1024 * 1024) return
            runOnUiThread {
                try {
                    val directory = File(cacheDir, "share").also { it.mkdirs() }
                    val file = File(directory, "PUK-Bond.private.json").also { it.writeText(json) }
                    val uri = FileProvider.getUriForFile(this@BondBuilderActivity, "$packageName.cache", file)
                    startActivity(Intent(this@BondBuilderActivity, MainActivity::class.java).apply {
                        action = Intent.ACTION_VIEW
                        setDataAndType(uri, "application/octet-stream")
                        addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
                    })
                } catch (e: Exception) { Toast.makeText(this@BondBuilderActivity, "导入失败：${e.message}", Toast.LENGTH_LONG).show() }
            }
        }
    }
    @Deprecated("Activity result bridge for file chooser")
    override fun onActivityResult(request: Int, result: Int, data: Intent?) {
        super.onActivityResult(request, result, data)
        if (request == 41) { chooser?.onReceiveValue(if (result == RESULT_OK && data?.data != null) arrayOf(data.data!!) else null); chooser = null }
        if (request == 42) {
            val text = pendingDocument; pendingDocument = null
            if (result == RESULT_OK && data?.data != null && text != null) {
                try { contentResolver.openOutputStream(data.data!!)?.use { it.write(text.toByteArray()) } }
                catch (e: Exception) { Toast.makeText(this, "保存失败：${e.message}", Toast.LENGTH_LONG).show() }
            }
        }
    }
    override fun onDestroy() { chooser?.onReceiveValue(null); web.removeJavascriptInterface("PUKHost"); web.destroy(); super.onDestroy() }
}
