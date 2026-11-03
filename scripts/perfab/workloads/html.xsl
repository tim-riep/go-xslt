<!-- HTML output method: a table row per record with several attributes per cell, escaping rules of HTML -->
<xsl:stylesheet version="1.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  <xsl:output method="html" indent="no"/>
  <xsl:template match="/">
    <html><head><title>orders</title><meta charset="utf-8"/></head><body>
      <table class="orders" border="1" cellspacing="0">
        <xsl:for-each select="orders/order">
          <tr id="{@id}" class="row {position() mod 2}" data-customer="{customer}" title="order {@id} &amp; {count(items/item)} items">
            <td class="c" align="left" data-n="{position()}"><a href="customers.html?name={customer}&amp;id={@id}"><xsl:value-of select="customer"/></a></td>
            <td class="d" nowrap="nowrap"><xsl:value-of select="date"/></td>
            <td class="p" align="right"><input type="text" name="p{@id}" value="{price}" disabled="disabled"/></td>
            <td><xsl:for-each select="items/item"><img src="/img/{@sku}.png" alt="{@sku}" width="16" height="16"/><br/></xsl:for-each></td>
          </tr>
        </xsl:for-each>
      </table>
    </body></html>
  </xsl:template>
</xsl:stylesheet>
