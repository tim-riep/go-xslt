<!-- XPath 1.0 predicates, string and number functions over every record -->
<xsl:stylesheet version="1.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  <xsl:output method="text"/>
  <xsl:template match="/">
    <xsl:value-of select="count(//order[price &gt; 500 and contains(customer, '7')])"/>
    <xsl:text> </xsl:text>
    <xsl:value-of select="sum(//order[starts-with(@id, 'o1')]/price)"/>
    <xsl:text> </xsl:text>
    <xsl:value-of select="string-length(translate(normalize-space(string(/)), '0123456789', ''))"/>
  </xsl:template>
</xsl:stylesheet>
